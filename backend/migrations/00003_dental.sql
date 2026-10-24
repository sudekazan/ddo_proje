-- +goose Up
CREATE TABLE "tn-tooth_status" (
  patient_id bigint NOT NULL REFERENCES "tn-patients"(id),
  tooth      smallint NOT NULL CHECK (fdi_valid(tooth)),
  state      text NOT NULL CHECK (state IN ('healthy','filled','root_canal','crowned','extracted','implant')),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (patient_id, tooth)
);

-- Diş bazlı zaman çizelgesi. Kayıtlar silinmez; yanlış tamamlama geri alınınca reverted_at işaretlenir.
CREATE TABLE "tn-tooth_events" (
  id             bigserial PRIMARY KEY,
  patient_id     bigint NOT NULL REFERENCES "tn-patients"(id),
  tooth          smallint NOT NULL CHECK (fdi_valid(tooth)),
  treatment_code text NOT NULL REFERENCES "tn-treatment_types"(code),
  state_before   text NOT NULL,
  state_after    text NOT NULL,
  performed_at   timestamptz NOT NULL,
  dentist_id     bigint REFERENCES "tn-staff"(id),
  appointment_id bigint REFERENCES "tn-appointments"(id),
  note           text NOT NULL DEFAULT '',
  reverted_at    timestamptz,
  created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tooth_events_timeline_idx ON "tn-tooth_events" (patient_id, tooth, performed_at DESC, id DESC);
CREATE INDEX tooth_events_appointment_idx ON "tn-tooth_events" (appointment_id) WHERE appointment_id IS NOT NULL;

-- +goose Down
DROP TABLE "tn-tooth_events";
DROP TABLE "tn-tooth_status";
