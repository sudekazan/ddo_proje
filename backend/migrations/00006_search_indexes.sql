-- +goose Up
-- Hasta arama: soyad öneki + keyset sayfalama.
CREATE INDEX patients_last_name_idx ON patients (lower(last_name) text_pattern_ops, id);
-- Rapor sorguları: tamamlanan randevuların tarihe göre taranması.
CREATE INDEX appointments_completed_idx ON appointments (completed_at) WHERE status = 'completed';

-- +goose Down
DROP INDEX appointments_completed_idx;
DROP INDEX patients_last_name_idx;
