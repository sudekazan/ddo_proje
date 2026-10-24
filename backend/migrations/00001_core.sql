-- +goose Up
CREATE EXTENSION IF NOT EXISTS btree_gist;

-- +goose StatementBegin
CREATE FUNCTION forbid_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION '% tablosu eklemeli-yalnızdır (append-only)', TG_TABLE_NAME
    USING ERRCODE = 'P0001';
END
$$;
-- +goose StatementEnd

-- FDI diş numarası: kalıcı 11-48, süt 51-85.
-- +goose StatementBegin
CREATE FUNCTION fdi_valid(t smallint) RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
  SELECT (t / 10 BETWEEN 1 AND 4 AND t % 10 BETWEEN 1 AND 8)
      OR (t / 10 BETWEEN 5 AND 8 AND t % 10 BETWEEN 1 AND 5)
$$;
-- +goose StatementEnd

CREATE TABLE users (
  id            bigserial PRIMARY KEY,
  email         text NOT NULL UNIQUE CHECK (email = lower(email) AND position('@' IN email) > 1),
  password_hash text NOT NULL,
  full_name     text NOT NULL CHECK (length(full_name) BETWEEN 1 AND 200),
  role          text NOT NULL CHECK (role IN ('patient','dentist','assistant','reception','accounting')),
  active        boolean NOT NULL DEFAULT true,
  created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE refresh_tokens (
  id         bigserial PRIMARY KEY,
  user_id    bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash bytea NOT NULL UNIQUE,
  expires_at timestamptz NOT NULL,
  revoked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX refresh_tokens_user_idx ON refresh_tokens (user_id) WHERE revoked_at IS NULL;

CREATE TABLE audit_log (
  id            bigserial PRIMARY KEY,
  at            timestamptz NOT NULL DEFAULT now(),
  actor_user_id bigint REFERENCES users(id),
  action        text NOT NULL,
  entity        text NOT NULL,
  entity_id     text NOT NULL,
  old_value     jsonb,
  new_value     jsonb,
  request_id    text
);
CREATE INDEX audit_log_entity_idx ON audit_log (entity, entity_id, id);
CREATE TRIGGER audit_log_append_only BEFORE UPDATE OR DELETE ON audit_log
  FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

CREATE TABLE idempotency_keys (
  user_id       bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  key           text NOT NULL CHECK (length(key) BETWEEN 1 AND 200),
  method        text NOT NULL,
  path          text NOT NULL,
  request_hash  bytea NOT NULL,
  status_code   int,
  response_body bytea,
  created_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, key)
);

-- +goose Down
DROP TABLE idempotency_keys;
DROP TABLE audit_log;
DROP TABLE refresh_tokens;
DROP TABLE users;
DROP FUNCTION fdi_valid(smallint);
DROP FUNCTION forbid_mutation();
