-- +goose Up
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.tuskrag_guard_current_version() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE target_status text;
BEGIN
  IF NEW.current_version_id IS NULL THEN
    RETURN NEW;
  END IF;
  IF TG_OP = 'UPDATE' AND NEW.current_version_id IS NOT DISTINCT FROM OLD.current_version_id THEN
    RETURN NEW;
  END IF;
  SELECT status INTO target_status FROM public.document_version
    WHERE tenant_id = NEW.tenant_id AND kb_id = NEW.kb_id AND document_id = NEW.id AND id = NEW.current_version_id
    FOR SHARE;
  IF NOT FOUND OR target_status NOT IN ('ready', 'partial') THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'current document version must be ready or partial';
  END IF;
  RETURN NEW;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.tuskrag_guard_version_status() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE current_id bigint;
BEGIN
  IF NEW.status IS NOT DISTINCT FROM OLD.status THEN
    RETURN NEW;
  END IF;
  IF OLD.status = 'purging' THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'purging document version cannot be restored';
  END IF;
  IF NEW.status NOT IN ('ready', 'partial') THEN
    SELECT current_version_id INTO current_id FROM public.document
      WHERE tenant_id = NEW.tenant_id AND kb_id = NEW.kb_id AND id = NEW.document_id FOR UPDATE;
    IF NOT FOUND THEN
      RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'document version has no owning document';
    END IF;
    IF current_id = NEW.id THEN
      RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'current document version must remain ready or partial';
    END IF;
  END IF;
  RETURN NEW;
END $$;
-- +goose StatementEnd
