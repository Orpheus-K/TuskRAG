package answer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"tuskrag.local/core/internal/dbquery"
	"tuskrag.local/core/internal/retrieval"
)

const (
	DefaultContextBudgetTokens = 6000
	parentBacktrackTokenLimit  = 1200
	similarityThreshold        = 0.95
	maxEvidencePayloadBytes    = 96 * 1024
	maxTokenCountSliceBytes    = 60 * 1024
	maxOverlapSearchBytes      = 1024
	evidenceMetadataTokenSlack = 24
)

// AssemblyOptions controls R07 context assembly over retrieved chunks.
type AssemblyOptions struct {
	ParentBacktrack     bool
	ContextBudgetTokens int
}

// DefaultAssemblyOptions returns the standard M2 R07 assembly policy.
func DefaultAssemblyOptions() AssemblyOptions {
	return AssemblyOptions{
		ParentBacktrack:     true,
		ContextBudgetTokens: DefaultContextBudgetTokens,
	}
}

type sectionChunk struct {
	ChunkID           int64
	DocumentID        int64
	VersionID         int64
	SectionID         int64
	Ordinal           int32
	Kind              string
	TokenCount        int32
	Content           string
	Title             string
	MIME              string
	HeadingPath       []string
	PageStart         *int32
	PageEnd           *int32
	SectionTokenCount int32
}

type sectionSnapshot struct {
	SectionID         int64
	SectionTokenCount int32
	AllAllowed        bool
	Chunks            []sectionChunk
}

type sectionLoader func(context.Context, retrieval.Actor, int64, []int64) (map[int64]sectionSnapshot, error)

type assembledUnit struct {
	SectionID int64
	Chunks    []sectionChunk
	Content   string
	Pages     []int32
	Tokens    int
	JSONBytes int
}

type assemblyPlan struct {
	Primary  assembledUnit
	Fallback *assembledUnit
}

func (s *Service) effectiveAssemblyOptions() AssemblyOptions {
	opts := s.Assembly
	if opts.ContextBudgetTokens <= 0 {
		opts.ContextBudgetTokens = DefaultContextBudgetTokens
	}
	return opts
}

func (s *Service) assemble(ctx context.Context, actor retrieval.Actor, kbID int64, candidates []retrieval.Candidate) ([]retrieval.Candidate, []AnswerEvidence, bool, error) {
	if len(candidates) == 0 {
		return []retrieval.Candidate{}, []AnswerEvidence{}, false, nil
	}
	if s.CountTokens == nil {
		return nil, nil, false, errors.New("answer context token counter is required")
	}
	opts := s.effectiveAssemblyOptions()
	unique := dedupeCandidateIDs(candidates)

	var sections map[int64]sectionSnapshot
	var err error
	if s.loadSections != nil {
		sections, err = s.loadSections(ctx, actor, kbID, collectSectionIDs(unique))
	} else if s.pool != nil {
		sections, err = loadSectionSnapshots(ctx, s.pool, actor, kbID, collectSectionIDs(unique))
	}
	if err != nil {
		return nil, nil, false, err
	}

	plans, err := s.buildAssemblyPlans(ctx, unique, sections, opts)
	if err != nil {
		return nil, nil, false, err
	}
	return packAssemblyPlans(plans, opts)
}

func dedupeCandidateIDs(candidates []retrieval.Candidate) []retrieval.Candidate {
	seen := make(map[int64]bool, len(candidates))
	result := make([]retrieval.Candidate, 0, len(candidates))
	for _, c := range candidates {
		if c.ChunkID > 0 && seen[c.ChunkID] {
			continue
		}
		if c.ChunkID > 0 {
			seen[c.ChunkID] = true
		}
		result = append(result, c)
	}
	return result
}

func collectSectionIDs(candidates []retrieval.Candidate) []int64 {
	seen := make(map[int64]bool, len(candidates))
	ids := make([]int64, 0, len(candidates))
	for _, c := range candidates {
		if c.SectionID > 0 && !seen[c.SectionID] {
			seen[c.SectionID] = true
			ids = append(ids, c.SectionID)
		}
	}
	return ids
}

func loadSectionSnapshots(ctx context.Context, pool *pgxpool.Pool, actor retrieval.Actor, kbID int64, sectionIDs []int64) (_ map[int64]sectionSnapshot, resultErr error) {
	result := make(map[int64]sectionSnapshot, len(sectionIDs))
	if len(sectionIDs) == 0 {
		return result, nil
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin context assembly transaction: %w", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tx.Rollback(cleanup); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			resultErr = errors.Join(resultErr, fmt.Errorf("rollback context assembly transaction: %w", err))
		}
	}()
	q := dbquery.New(tx)
	if _, err := q.SetTenant(ctx, actor.TenantID); err != nil {
		return nil, fmt.Errorf("set context assembly tenant: %w", err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL plan_cache_mode = force_custom_plan`); err != nil {
		return nil, fmt.Errorf("set context assembly plan mode: %w", err)
	}
	if _, err := q.LockSearchBinding(ctx, dbquery.LockSearchBindingParams{TenantID: actor.TenantID, KbID: kbID, UserID: actor.UserID}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, retrieval.ErrNotFound
		}
		return nil, fmt.Errorf("lock context assembly binding: %w", err)
	}
	principal, err := retrieval.PrincipalTags(ctx, q, actor.TenantID, kbID, actor.UserID)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListSectionChunksForAnswer(ctx, dbquery.ListSectionChunksForAnswerParams{
		TenantID:      actor.TenantID,
		KbID:          kbID,
		UserID:        actor.UserID,
		SectionIds:    sectionIDs,
		PrincipalTags: principal,
	})
	if err != nil {
		return nil, fmt.Errorf("list section chunks: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit context assembly transaction: %w", err)
	}

	for _, r := range rows {
		if r.PageStart.Valid != r.PageEnd.Valid {
			return nil, errors.New("section chunk has an invalid page range")
		}
		snap, ok := result[r.SectionID]
		if !ok {
			snap = sectionSnapshot{
				SectionID:         r.SectionID,
				SectionTokenCount: r.SectionTokenCount,
				AllAllowed:        true,
			}
		}
		if !r.Allowed {
			snap.AllAllowed = false
			result[r.SectionID] = snap
			continue
		}
		chunk := sectionChunk{
			ChunkID:           r.ID,
			DocumentID:        r.DocumentID,
			VersionID:         r.VersionID,
			SectionID:         r.SectionID,
			Ordinal:           r.Ordinal,
			Kind:              r.Kind,
			TokenCount:        r.TokenCount,
			Content:           r.Content,
			Title:             r.Title,
			MIME:              r.Mime,
			HeadingPath:       append([]string{}, r.HeadingPath...),
			SectionTokenCount: r.SectionTokenCount,
		}
		if r.PageStart.Valid && r.Mime == "application/pdf" {
			ps, pe := r.PageStart.Int32, r.PageEnd.Int32
			chunk.PageStart, chunk.PageEnd = &ps, &pe
		}
		snap.Chunks = append(snap.Chunks, chunk)
		result[r.SectionID] = snap
	}
	return result, nil
}

func (s *Service) buildAssemblyPlans(ctx context.Context, candidates []retrieval.Candidate, sections map[int64]sectionSnapshot, opts AssemblyOptions) ([]assemblyPlan, error) {
	hitsBySection := make(map[int64][]int)
	for i, c := range candidates {
		if c.SectionID > 0 {
			hitsBySection[c.SectionID] = append(hitsBySection[c.SectionID], i)
		}
	}

	usedIdx := make([]bool, len(candidates))
	plans := make([]assemblyPlan, 0, len(candidates))
	for i, c := range candidates {
		if usedIdx[i] {
			continue
		}
		if c.SectionID <= 0 {
			usedIdx[i] = true
			unit, err := s.measureUnit(ctx, []sectionChunk{candidateToChunk(c)})
			if err != nil {
				return nil, err
			}
			plans = append(plans, assemblyPlan{Primary: unit})
			continue
		}

		indices := hitsBySection[c.SectionID]
		snap, hasSnap := sections[c.SectionID]
		if !hasSnap && sections != nil {
			// Section no longer visible/valid in current snapshot; drop candidate safely.
			for _, idx := range indices {
				usedIdx[idx] = true
			}
			continue
		}

		// Map allowed chunks in this section when snapshot exists.
		allowedByID := map[int64]sectionChunk{}
		if hasSnap {
			for _, ch := range snap.Chunks {
				allowedByID[ch.ChunkID] = ch
			}
		}

		// Collect authorized hit chunks in this section.
		hitChunks := make([]sectionChunk, 0, len(indices))
		for _, idx := range indices {
			cand := candidates[idx]
			if hasSnap {
				ch, ok := allowedByID[cand.ChunkID]
				if !ok {
					continue
				}
				hitChunks = append(hitChunks, ch)
			} else {
				hitChunks = append(hitChunks, candidateToChunk(cand))
			}
		}
		if len(hitChunks) == 0 {
			for _, idx := range indices {
				usedIdx[idx] = true
			}
			continue
		}

		// Parent section backtracking check.
		shouldBacktrack := opts.ParentBacktrack && hasSnap && snap.AllAllowed && len(snap.Chunks) > 0 &&
			(len(hitChunks) >= 2 || snap.SectionTokenCount <= parentBacktrackTokenLimit)

		if shouldBacktrack {
			for _, idx := range indices {
				usedIdx[idx] = true
			}
			fullUnit, err := s.measureUnit(ctx, snap.Chunks)
			if err != nil {
				return nil, err
			}
			fallbackChunks := contiguousRunAt(hitChunks, snap.Chunks, candidates[i].ChunkID)
			fallbackUnit, err := s.measureUnit(ctx, fallbackChunks)
			if err != nil {
				return nil, err
			}
			var fallback *assembledUnit
			if len(fallbackChunks) < len(snap.Chunks) {
				fallback = &fallbackUnit
			}
			plans = append(plans, assemblyPlan{Primary: fullUnit, Fallback: fallback})
			continue
		}

		// Neighbor merging: find contiguous ordinal run containing candidates[i].
		var sectionOrder []sectionChunk
		if hasSnap {
			sectionOrder = snap.Chunks
		}
		run := contiguousRunAt(hitChunks, sectionOrder, candidates[i].ChunkID)
		runIDs := make(map[int64]bool, len(run))
		for _, ch := range run {
			runIDs[ch.ChunkID] = true
		}
		for _, idx := range indices {
			if runIDs[candidates[idx].ChunkID] {
				usedIdx[idx] = true
			}
		}
		usedIdx[i] = true
		unit, err := s.measureUnit(ctx, run)
		if err != nil {
			return nil, err
		}
		plans = append(plans, assemblyPlan{Primary: unit})
	}
	return plans, nil
}

func contiguousRunAt(hits []sectionChunk, fullSection []sectionChunk, anchorID int64) []sectionChunk {
	if len(hits) <= 1 {
		return append([]sectionChunk(nil), hits...)
	}
	sortedHits := append([]sectionChunk(nil), hits...)
	sort.Slice(sortedHits, func(i, j int) bool {
		if sortedHits[i].Ordinal != sortedHits[j].Ordinal {
			return sortedHits[i].Ordinal < sortedHits[j].Ordinal
		}
		return sortedHits[i].ChunkID < sortedHits[j].ChunkID
	})

	// Build ordinal -> gap check if fullSection is known (to ensure no disabled chunk sits between hits).
	allowedOrdinals := map[int32]bool{}
	if len(fullSection) > 0 {
		for _, ch := range fullSection {
			allowedOrdinals[ch.Ordinal] = true
		}
	}

	anchorPos := -1
	for idx, ch := range sortedHits {
		if ch.ChunkID == anchorID {
			anchorPos = idx
			break
		}
	}
	if anchorPos < 0 {
		return sortedHits[:1]
	}

	areAdjacent := func(a, b sectionChunk) bool {
		if a.Ordinal <= 0 || b.Ordinal <= 0 {
			return false
		}
		if b.Ordinal != a.Ordinal+1 {
			return false
		}
		if len(allowedOrdinals) > 0 {
			return allowedOrdinals[a.Ordinal] && allowedOrdinals[b.Ordinal]
		}
		return true
	}

	left, right := anchorPos, anchorPos
	for left > 0 && areAdjacent(sortedHits[left-1], sortedHits[left]) {
		left--
	}
	for right+1 < len(sortedHits) && areAdjacent(sortedHits[right], sortedHits[right+1]) {
		right++
	}
	return append([]sectionChunk(nil), sortedHits[left:right+1]...)
}

func (s *Service) measureUnit(ctx context.Context, chunks []sectionChunk) (assembledUnit, error) {
	sorted := append([]sectionChunk(nil), chunks...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Ordinal != sorted[j].Ordinal {
			return sorted[i].Ordinal < sorted[j].Ordinal
		}
		return sorted[i].ChunkID < sorted[j].ChunkID
	})
	content := stitchChunks(sorted)
	pages := aggregatePages(sorted)
	first := sorted[0]

	framed := first.Title
	if len(first.HeadingPath) > 0 {
		framed += " › " + strings.Join(first.HeadingPath, " › ")
	}
	framed += "\n" + content
	tokens, err := countPromptTokens(ctx, s.CountTokens, framed)
	if err != nil {
		return assembledUnit{}, err
	}
	tokens += evidenceMetadataTokenSlack

	marshaled, err := json.Marshal(AnswerEvidence{
		N:           1,
		ID:          fmt.Sprintf("c_%d", first.ChunkID),
		Content:     content,
		DocumentID:  first.DocumentID,
		VersionID:   first.VersionID,
		Title:       first.Title,
		SectionID:   first.SectionID,
		HeadingPath: append([]string{}, first.HeadingPath...),
		Pages:       pages,
	})
	if err != nil {
		return assembledUnit{}, err
	}
	return assembledUnit{
		SectionID: first.SectionID,
		Chunks:    sorted,
		Content:   content,
		Pages:     pages,
		Tokens:    tokens,
		JSONBytes: len(marshaled) + 1,
	}, nil
}

func packAssemblyPlans(plans []assemblyPlan, opts AssemblyOptions) ([]retrieval.Candidate, []AnswerEvidence, bool, error) {
	selected := make([]retrieval.Candidate, 0, len(plans))
	evidence := make([]AnswerEvidence, 0, len(plans))
	seenHashes := make(map[[32]byte]bool, len(plans))
	seenChunkIDs := make(map[int64]bool, len(plans))
	acceptedNorms := make([]normDocument, 0, len(plans))

	tokenBudget := opts.ContextBudgetTokens
	byteBudget := maxEvidencePayloadBytes
	limited := false

	isDuplicate := func(unit assembledUnit) bool {
		digest := sha256.Sum256([]byte(unit.Content))
		if seenHashes[digest] {
			return true
		}
		for _, ch := range unit.Chunks {
			if ch.ChunkID > 0 && seenChunkIDs[ch.ChunkID] {
				return true
			}
		}
		norm := prepareNormDocument(unit.Content)
		for _, existing := range acceptedNorms {
			if trigramDiceSimilarity(existing, norm) > similarityThreshold {
				return true
			}
		}
		return false
	}

	fits := func(unit assembledUnit) bool {
		return unit.Tokens <= tokenBudget && unit.JSONBytes <= byteBudget
	}

	accept := func(unit assembledUnit) {
		first := unit.Chunks[0]
		ids := make([]int64, len(unit.Chunks))
		for idx, ch := range unit.Chunks {
			ids[idx] = ch.ChunkID
			if ch.ChunkID > 0 {
				seenChunkIDs[ch.ChunkID] = true
			}
			selected = append(selected, chunkToCandidate(ch))
		}
		seenHashes[sha256.Sum256([]byte(unit.Content))] = true
		acceptedNorms = append(acceptedNorms, prepareNormDocument(unit.Content))
		tokenBudget -= unit.Tokens
		byteBudget -= unit.JSONBytes
		evidence = append(evidence, AnswerEvidence{
			N:           len(evidence) + 1,
			ID:          fmt.Sprintf("c_%d", first.ChunkID),
			ChunkIDs:    ids,
			Content:     unit.Content,
			DocumentID:  first.DocumentID,
			VersionID:   first.VersionID,
			Title:       first.Title,
			SectionID:   first.SectionID,
			HeadingPath: append([]string{}, first.HeadingPath...),
			Pages:       unit.Pages,
		})
	}

	for _, plan := range plans {
		if isDuplicate(plan.Primary) {
			continue
		}
		if fits(plan.Primary) {
			accept(plan.Primary)
			continue
		}
		if plan.Fallback != nil && !isDuplicate(*plan.Fallback) && fits(*plan.Fallback) {
			accept(*plan.Fallback)
			continue
		}
		limited = true
		break
	}
	return selected, evidence, limited, nil
}

func stitchChunks(chunks []sectionChunk) string {
	if len(chunks) == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteString(chunks[0].Content)
	prevKind := chunks[0].Kind
	for i := 1; i < len(chunks); i++ {
		curr := chunks[i]
		if prevKind == "table" && curr.Kind == "table" {
			if body, ok := stripMatchingTableHeader(out.String(), curr.Content); ok {
				if body != "" {
					out.WriteByte('\n')
					out.WriteString(body)
				}
				prevKind = curr.Kind
				continue
			}
		}
		if (prevKind == "text" || prevKind == "") && (curr.Kind == "text" || curr.Kind == "") {
			if overlap := findTextOverlap(out.String(), curr.Content); overlap > 0 {
				out.WriteString(curr.Content[overlap:])
				prevKind = curr.Kind
				continue
			}
		}
		out.WriteString("\n\n")
		out.WriteString(curr.Content)
		prevKind = curr.Kind
	}
	return out.String()
}

func stripMatchingTableHeader(prev, curr string) (string, bool) {
	prevHeader, ok1 := tableHeaderLines(prev)
	currHeader, ok2 := tableHeaderLines(curr)
	if !ok1 || !ok2 || prevHeader != currHeader {
		return "", false
	}
	rest := curr[len(currHeader):]
	rest = strings.TrimPrefix(rest, "\n")
	return rest, true
}

func tableHeaderLines(text string) (string, bool) {
	firstNL := strings.IndexByte(text, '\n')
	if firstNL <= 0 {
		return "", false
	}
	secondRel := strings.IndexByte(text[firstNL+1:], '\n')
	var secondEnd int
	if secondRel < 0 {
		secondEnd = len(text)
	} else {
		secondEnd = firstNL + 1 + secondRel
	}
	line1 := strings.TrimSpace(text[:firstNL])
	line2 := strings.TrimSpace(text[firstNL+1 : secondEnd])
	if !strings.HasPrefix(line1, "|") || !strings.HasPrefix(line2, "|") || !strings.Contains(line2, "---") {
		return "", false
	}
	return text[:secondEnd], true
}

func findTextOverlap(prev, curr string) int {
	limit := min(len(prev), len(curr), maxOverlapSearchBytes)
	if limit == 0 {
		return 0
	}
	// Try longest matching suffix of prev / prefix of curr down to shortest sentence.
	for l := limit; l > 0; l-- {
		if l < len(curr) && !utf8.RuneStart(curr[l]) {
			continue
		}
		if l == len(curr) && !utf8.ValidString(curr) {
			continue
		}
		prevStart := len(prev) - l
		if !utf8.RuneStart(prev[prevStart]) {
			continue
		}
		candidate := curr[:l]
		if prev[prevStart:] != candidate {
			continue
		}
		if !validOverlapBoundary(prev, prevStart, candidate) {
			continue
		}
		return l
	}
	return 0
}

func validOverlapBoundary(prev string, prevStart int, overlap string) bool {
	if !endsAtSentenceBoundary(overlap) {
		return false
	}
	if prevStart == 0 {
		return true
	}
	before := prev[:prevStart]
	if strings.HasSuffix(before, "\n\n") || strings.HasSuffix(before, "\n") {
		return true
	}
	return endsAtSentenceBoundary(before)
}

func endsAtSentenceBoundary(text string) bool {
	if strings.HasSuffix(text, "\n") {
		return true
	}
	trimmed := strings.TrimRightFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("\"'”’）)】]」』》", r)
	})
	if trimmed == "" {
		return false
	}
	last, _ := utf8.DecodeLastRuneInString(trimmed)
	return strings.ContainsRune("。！？!?.", last)
}

func aggregatePages(chunks []sectionChunk) []int32 {
	var minPage, maxPage int32
	hasPages := false
	for _, ch := range chunks {
		if ch.PageStart == nil || ch.PageEnd == nil {
			continue
		}
		if !hasPages {
			minPage, maxPage = *ch.PageStart, *ch.PageEnd
			hasPages = true
			continue
		}
		if *ch.PageStart < minPage {
			minPage = *ch.PageStart
		}
		if *ch.PageEnd > maxPage {
			maxPage = *ch.PageEnd
		}
	}
	if !hasPages {
		return nil
	}
	return []int32{minPage, maxPage}
}

func countPromptTokens(ctx context.Context, counter retrieval.TokenCounter, text string) (int, error) {
	if text == "" {
		return 0, nil
	}
	if len(text) <= maxTokenCountSliceBytes {
		return counter(ctx, text)
	}
	total := 0
	for len(text) > 0 {
		if len(text) <= maxTokenCountSliceBytes {
			n, err := counter(ctx, text)
			if err != nil {
				return 0, err
			}
			return total + n, nil
		}
		cut := maxTokenCountSliceBytes
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		if cut == 0 {
			return 0, errors.New("invalid UTF-8 sequence in evidence")
		}
		n, err := counter(ctx, text[:cut])
		if err != nil {
			return 0, err
		}
		total += n
		text = text[cut:]
	}
	return total, nil
}

type normDocument struct {
	normalized string
	trigrams   map[[3]rune]int
	total      int
}

func prepareNormDocument(text string) normDocument {
	fields := strings.Fields(strings.ToLower(text))
	normalized := strings.Join(fields, " ")
	runes := []rune(normalized)
	doc := normDocument{normalized: normalized}
	if len(runes) < 3 {
		return doc
	}
	trigrams := make(map[[3]rune]int, len(runes)-2)
	for i := 0; i <= len(runes)-3; i++ {
		key := [3]rune{runes[i], runes[i+1], runes[i+2]}
		trigrams[key]++
	}
	doc.trigrams = trigrams
	doc.total = len(runes) - 2
	return doc
}

func trigramDiceSimilarity(a, b normDocument) float64 {
	if a.normalized == b.normalized {
		return 1
	}
	if a.total == 0 || b.total == 0 {
		return 0
	}
	// Quick length-ratio prune: 2*min(a,b)/(a+b) <= threshold implies similarity cannot exceed threshold.
	minTotal, maxTotal := a.total, b.total
	if minTotal > maxTotal {
		minTotal, maxTotal = maxTotal, minTotal
	}
	if float64(2*minTotal)/float64(minTotal+maxTotal) <= similarityThreshold {
		return 0
	}
	smaller, larger := a.trigrams, b.trigrams
	if len(smaller) > len(larger) {
		smaller, larger = larger, smaller
	}
	intersection := 0
	for k, ca := range smaller {
		if cb, ok := larger[k]; ok {
			intersection += min(ca, cb)
		}
	}
	return float64(2*intersection) / float64(a.total+b.total)
}

func candidateToChunk(c retrieval.Candidate) sectionChunk {
	return sectionChunk{
		ChunkID:     c.ChunkID,
		DocumentID:  c.DocumentID,
		VersionID:   c.VersionID,
		SectionID:   c.SectionID,
		Ordinal:     c.Ordinal,
		Kind:        c.Kind,
		TokenCount:  c.TokenCount,
		Content:     c.Content,
		Title:       c.Title,
		MIME:        c.MIME,
		HeadingPath: append([]string{}, c.HeadingPath...),
		PageStart:   c.PageStart,
		PageEnd:     c.PageEnd,
	}
}

func chunkToCandidate(ch sectionChunk) retrieval.Candidate {
	return retrieval.Candidate{
		ChunkID:     ch.ChunkID,
		DocumentID:  ch.DocumentID,
		VersionID:   ch.VersionID,
		SectionID:   ch.SectionID,
		Ordinal:     ch.Ordinal,
		Kind:        ch.Kind,
		TokenCount:  ch.TokenCount,
		MIME:        ch.MIME,
		Title:       ch.Title,
		Content:     ch.Content,
		HeadingPath: append([]string{}, ch.HeadingPath...),
		PageStart:   ch.PageStart,
		PageEnd:     ch.PageEnd,
	}
}
