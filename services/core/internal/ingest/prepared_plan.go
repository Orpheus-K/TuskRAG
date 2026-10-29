package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash"
	"slices"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalidPlan  = errors.New("invalid prepared ingest plan")
	ErrPlanLimit    = errors.New("prepared ingest plan exceeds size limit")
	ErrPlanConflict = errors.New("a different ingest plan is already staged")
)

const (
	maxPlanSections  = 10000
	maxPlanChunks    = 10000
	maxPlanRawBytes  = 64 * 1024 * 1024
	maxPlanRowBytes  = 4 * 1024 * 1024
	maxPlanJSONBytes = 128 * 1024 * 1024
)

// PreparedPlan is produced before opening the staging transaction. Its slices
// and strings must not be modified while Stage is running.
type PreparedPlan struct {
	EmbeddingSpaceID int64
	EmbeddingProfile EmbeddingProfile
	ConfigRevision   int32
	Sections         []PreparedSection
	Chunks           []PreparedChunk
}

type EmbeddingProfile struct {
	ProviderID int64
	Model      string
	Dimensions int32
	Normalized bool
	Distance   string
}

// ParentIndex is -1 for a root, otherwise a zero-based earlier section.
type PreparedSection struct {
	ParentIndex  int
	Level        int16
	Heading      string
	HeadingPath  []string
	PageStart    int32
	PageEnd      int32
	BodyMarkdown string
	TokenCount   int32
}

// SectionIndex is zero-based in PreparedPlan.Sections.
type PreparedChunk struct {
	SectionIndex int
	Kind         string
	Content      string
	SearchText   string
	PageStart    int32
	PageEnd      int32
	TokenCount   int32
}

type planDigest struct {
	hash hash.Hash
	buf  [8]byte
}

func (d *planDigest) number(value int64) {
	binary.BigEndian.PutUint64(d.buf[:], uint64(value))
	_, _ = d.hash.Write(d.buf[:])
}

func (d *planDigest) text(value string) {
	d.number(int64(len(value)))
	_, _ = d.hash.Write([]byte(value))
}

type planHashes struct {
	Digest  [sha256.Size]byte
	Content [sha256.Size]byte
}

func validatePreparedPlan(ctx context.Context, plan PreparedPlan) (planHashes, error) {
	var zero planHashes
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if plan.EmbeddingSpaceID <= 0 || plan.ConfigRevision <= 0 || len(plan.Sections) == 0 || len(plan.Chunks) == 0 || !validEmbeddingProfile(plan.EmbeddingProfile) {
		return zero, ErrInvalidPlan
	}
	if len(plan.Sections) > maxPlanSections || len(plan.Chunks) > maxPlanChunks {
		return zero, ErrPlanLimit
	}
	var total int
	add := func(value string, row *int) error {
		if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
			return ErrInvalidPlan
		}
		if len(value) > maxPlanRawBytes-total || len(value) > maxPlanRowBytes-*row {
			return ErrPlanLimit
		}
		total += len(value)
		*row += len(value)
		return nil
	}
	digest := planDigest{hash: sha256.New()}
	digest.text("tuskrag-ingest-content-v3")
	digest.number(int64(len(plan.Sections)))
	for index, section := range plan.Sections {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		if section.ParentIndex < -1 || section.ParentIndex >= index || section.PageStart <= 0 || section.PageEnd < section.PageStart ||
			section.TokenCount < 0 || (section.BodyMarkdown == "") != (section.TokenCount == 0) || len(section.HeadingPath) > 6 {
			return zero, ErrInvalidPlan
		}
		if section.Level == 0 {
			if section.ParentIndex != -1 || section.Heading != "" || len(section.HeadingPath) != 0 {
				return zero, ErrInvalidPlan
			}
		} else if section.Level < 1 || section.Level > 6 || strings.TrimSpace(section.Heading) == "" ||
			len(section.HeadingPath) == 0 || section.HeadingPath[len(section.HeadingPath)-1] != section.Heading {
			return zero, ErrInvalidPlan
		}
		if section.ParentIndex >= 0 {
			parent := plan.Sections[section.ParentIndex]
			if parent.Level == 0 || section.PageStart < parent.PageStart || section.PageEnd > parent.PageEnd {
				return zero, ErrInvalidPlan
			}
			samePath := slices.Equal(section.HeadingPath, parent.HeadingPath)
			childPath := len(section.HeadingPath) == len(parent.HeadingPath)+1 && slices.Equal(section.HeadingPath[:len(parent.HeadingPath)], parent.HeadingPath)
			validSame := samePath && section.Level == parent.Level
			validChild := childPath && section.Level > parent.Level
			if !validSame && !validChild {
				return zero, ErrInvalidPlan
			}
		} else if section.Level > 0 && len(section.HeadingPath) != 1 {
			return zero, ErrInvalidPlan
		}
		row := 0
		for _, value := range []string{section.Heading, section.BodyMarkdown} {
			if err := add(value, &row); err != nil {
				return zero, err
			}
		}
		if utf8.RuneCountInString(section.Heading) > maxNormalizedField {
			return zero, ErrPlanLimit
		}
		digest.number(int64(section.ParentIndex))
		digest.number(int64(section.Level))
		digest.text(section.Heading)
		digest.number(int64(len(section.HeadingPath)))
		for _, heading := range section.HeadingPath {
			if strings.TrimSpace(heading) == "" {
				return zero, ErrInvalidPlan
			}
			if err := add(heading, &row); err != nil {
				return zero, err
			}
			digest.text(heading)
		}
		digest.number(int64(section.PageStart))
		digest.number(int64(section.PageEnd))
		digest.text(section.BodyMarkdown)
		digest.number(int64(section.TokenCount))
	}
	digest.number(int64(len(plan.Chunks)))
	for _, chunk := range plan.Chunks {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		if chunk.SectionIndex < 0 || chunk.SectionIndex >= len(plan.Sections) || chunk.PageStart <= 0 || chunk.PageEnd < chunk.PageStart ||
			chunk.TokenCount <= 0 || chunk.TokenCount > maxTextChunkTokens || chunk.Content == "" || chunk.SearchText == "" {
			return zero, ErrInvalidPlan
		}
		switch chunk.Kind {
		case "text", "table", "list", "code", "figure_caption":
		default:
			return zero, ErrInvalidPlan
		}
		section := plan.Sections[chunk.SectionIndex]
		if chunk.PageStart < section.PageStart || chunk.PageEnd > section.PageEnd {
			return zero, ErrInvalidPlan
		}
		row := 0
		for _, value := range []string{chunk.Kind, chunk.Content, chunk.SearchText} {
			if err := add(value, &row); err != nil {
				return zero, err
			}
		}
		prefix := strings.Join(section.HeadingPath, " › ")
		if prefix != "" {
			prefix += "\n"
		}
		if !strings.HasPrefix(chunk.SearchText, prefix+chunk.Content) {
			return zero, ErrInvalidPlan
		}
		digest.number(int64(chunk.SectionIndex))
		digest.text(chunk.Kind)
		digest.text(chunk.Content)
		digest.text(chunk.SearchText)
		digest.number(int64(chunk.PageStart))
		digest.number(int64(chunk.PageEnd))
		digest.number(int64(chunk.TokenCount))
	}
	var result planHashes
	copy(result.Content[:], digest.hash.Sum(nil))
	result.Digest = planIdentityDigest(PlanIdentity{ContentDigest: result.Content, EmbeddingSpaceID: plan.EmbeddingSpaceID,
		EmbeddingProfile: plan.EmbeddingProfile, ConfigRevision: plan.ConfigRevision,
		SectionCount: int32(len(plan.Sections)), ChunkCount: int32(len(plan.Chunks))})
	return result, nil
}

func planIdentityDigest(identity PlanIdentity) [sha256.Size]byte {
	digest := planDigest{hash: sha256.New()}
	digest.text("tuskrag-ingest-plan-v3")
	digest.number(identity.EmbeddingSpaceID)
	digest.number(identity.EmbeddingProfile.ProviderID)
	digest.text(identity.EmbeddingProfile.Model)
	digest.number(int64(identity.EmbeddingProfile.Dimensions))
	if identity.EmbeddingProfile.Normalized {
		digest.number(1)
	} else {
		digest.number(0)
	}
	digest.text(identity.EmbeddingProfile.Distance)
	digest.number(int64(identity.ConfigRevision))
	digest.number(int64(identity.SectionCount))
	digest.number(int64(identity.ChunkCount))
	digest.number(int64(len(identity.ContentDigest)))
	_, _ = digest.hash.Write(identity.ContentDigest[:])
	var result [sha256.Size]byte
	copy(result[:], digest.hash.Sum(nil))
	return result
}

func validEmbeddingProfile(profile EmbeddingProfile) bool {
	return profile.ProviderID > 0 && profile.Dimensions >= 64 && profile.Dimensions <= 4000 &&
		(profile.Distance == "cosine" || profile.Distance == "ip") && len(profile.Model) <= 4096 &&
		utf8.ValidString(profile.Model) && !strings.ContainsRune(profile.Model, '\x00') && strings.TrimSpace(profile.Model) != ""
}
