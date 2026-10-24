-- +goose Up
CREATE TABLE "tn-institutions" (
  id          bigserial PRIMARY KEY,
  name        text NOT NULL UNIQUE,
  discount_bp int NOT NULL CHECK (discount_bp BETWEEN 0 AND 10000) -- baz puan: 1500 = %15
);

CREATE TABLE "tn-patients" (
  id             bigserial PRIMARY KEY,
  national_id    char(11) NOT NULL CONSTRAINT patients_national_id_key UNIQUE CHECK (national_id ~ '^[1-9][0-9]{10}$'),
  first_name     text NOT NULL CHECK (length(first_name) BETWEEN 1 AND 100),
  last_name      text NOT NULL CHECK (length(last_name) BETWEEN 1 AND 100),
  birth_date     date NOT NULL CHECK (birth_date > '1900-01-01'),
  phone          text NOT NULL CHECK (phone ~ '^\+?[0-9]{10,15}$'),
  email          text CHECK (email IS NULL OR position('@' IN email) > 1),
  institution_id bigint REFERENCES "tn-institutions"(id),
  user_id        bigint UNIQUE REFERENCES "tn-dk-users"(id),
  created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE "tn-staff" (
  id        bigserial PRIMARY KEY,
  full_name text NOT NULL,
  kind      text NOT NULL CHECK (kind IN ('dentist','assistant')),
  user_id   bigint UNIQUE REFERENCES "tn-dk-users"(id),
  active    boolean NOT NULL DEFAULT true
);

CREATE TABLE "tn-units" (
  id     bigserial PRIMARY KEY,
  name   text NOT NULL UNIQUE,
  active boolean NOT NULL DEFAULT true
);

CREATE TABLE "tn-staff_leaves" (
  id       bigserial PRIMARY KEY,
  staff_id bigint NOT NULL REFERENCES "tn-staff"(id),
  during   tstzrange NOT NULL CHECK (NOT isempty(during)),
  reason   text NOT NULL DEFAULT '',
  CONSTRAINT staff_leaves_no_overlap EXCLUDE USING gist (staff_id WITH =, during WITH &&)
);

CREATE TABLE "tn-treatment_types" (
  code           text PRIMARY KEY CHECK (code ~ '^[A-Z_]+$'),
  name           text NOT NULL,
  duration_min   int NOT NULL CHECK (duration_min BETWEEN 15 AND 240 AND duration_min % 15 = 0),
  sessions       int NOT NULL DEFAULT 1 CHECK (sessions BETWEEN 1 AND 10),
  min_gap_days   int NOT NULL DEFAULT 0 CHECK (min_gap_days >= 0),
  requires_tooth boolean NOT NULL
);

-- Tedavi planları randevulardan önce tanımlanır; randevular plan kalemine bağlanır.
CREATE TABLE "tn-treatment_plans" (
  id              bigserial PRIMARY KEY,
  patient_id      bigint NOT NULL REFERENCES "tn-patients"(id),
  dentist_id      bigint NOT NULL REFERENCES "tn-staff"(id),
  status          text NOT NULL DEFAULT 'draft'
                  CHECK (status IN ('draft','approved','in_progress','completed','cancelled')),
  current_version int NOT NULL DEFAULT 1 CHECK (current_version >= 1),
  created_at      timestamptz NOT NULL DEFAULT now(),
  cancelled_at    timestamptz
);
CREATE INDEX treatment_plans_patient_idx ON "tn-treatment_plans" (patient_id, id DESC);

CREATE TABLE "tn-plan_versions" (
  plan_id         bigint NOT NULL REFERENCES "tn-treatment_plans"(id),
  version         int NOT NULL CHECK (version >= 1),
  price_date      date NOT NULL,
  discount_bp     int NOT NULL CHECK (discount_bp BETWEEN 0 AND 10000),
  gross_kurus     bigint NOT NULL CHECK (gross_kurus >= 0),
  discount_kurus  bigint NOT NULL CHECK (discount_kurus >= 0),
  net_kurus       bigint NOT NULL CHECK (net_kurus = gross_kurus - discount_kurus),
  note            text NOT NULL DEFAULT '',
  created_by      bigint REFERENCES "tn-dk-users"(id),
  created_at      timestamptz NOT NULL DEFAULT now(),
  approved_at     timestamptz,
  approved_by     bigint REFERENCES "tn-dk-users"(id),
  approval_method text CHECK (approval_method IN ('patient_portal','signed_form')),
  PRIMARY KEY (plan_id, version),
  CHECK ((approved_at IS NULL) = (approval_method IS NULL))
);

CREATE TABLE "tn-plan_items" (
  id               bigserial PRIMARY KEY,
  plan_id          bigint NOT NULL,
  version          int NOT NULL,
  seq              int NOT NULL CHECK (seq >= 1),
  tooth            smallint CHECK (tooth IS NULL OR fdi_valid(tooth)),
  treatment_code   text NOT NULL REFERENCES "tn-treatment_types"(code),
  depends_on       bigint REFERENCES "tn-plan_items"(id),
  sessions_total   int NOT NULL CHECK (sessions_total >= 1),
  sessions_done    int NOT NULL DEFAULT 0 CHECK (sessions_done BETWEEN 0 AND sessions_total),
  unit_price_kurus bigint NOT NULL CHECK (unit_price_kurus >= 0),
  discount_kurus   bigint NOT NULL CHECK (discount_kurus BETWEEN 0 AND unit_price_kurus),
  net_kurus        bigint NOT NULL CHECK (net_kurus = unit_price_kurus - discount_kurus),
  billed_kurus     bigint NOT NULL DEFAULT 0 CHECK (billed_kurus BETWEEN 0 AND net_kurus),
  status           text NOT NULL DEFAULT 'pending'
                   CHECK (status IN ('pending','in_progress','done','cancelled')),
  FOREIGN KEY (plan_id, version) REFERENCES "tn-plan_versions"(plan_id, version),
  UNIQUE (plan_id, version, seq),
  CHECK (depends_on IS NULL OR depends_on <> id)
);

CREATE TABLE "tn-appointments" (
  id             bigserial PRIMARY KEY,
  patient_id     bigint NOT NULL REFERENCES "tn-patients"(id),
  dentist_id     bigint NOT NULL REFERENCES "tn-staff"(id),
  assistant_id   bigint NOT NULL REFERENCES "tn-staff"(id),
  unit_id        bigint NOT NULL REFERENCES "tn-units"(id),
  treatment_code text NOT NULL REFERENCES "tn-treatment_types"(code),
  tooth          smallint CHECK (tooth IS NULL OR fdi_valid(tooth)),
  plan_item_id   bigint REFERENCES "tn-plan_items"(id),
  session_no     int CHECK (session_no >= 1),
  during         tstzrange NOT NULL
                 CHECK (NOT isempty(during) AND lower_inc(during) AND NOT upper_inc(during)),
  status         text NOT NULL DEFAULT 'booked'
                 CHECK (status IN ('booked','completed','cancelled','no_show')),
  cancel_reason  text,
  created_by     bigint REFERENCES "tn-dk-users"(id),
  created_at     timestamptz NOT NULL DEFAULT now(),
  completed_at   timestamptz,
  CHECK ((plan_item_id IS NULL) = (session_no IS NULL)),
  CHECK (dentist_id <> assistant_id),
  -- Çakışma garantisi veritabanında: aynı kaynak aynı anda iki aktif randevuda olamaz.
  CONSTRAINT appt_no_dentist_overlap   EXCLUDE USING gist (dentist_id WITH =, during WITH &&)   WHERE (status IN ('booked','completed')),
  CONSTRAINT appt_no_assistant_overlap EXCLUDE USING gist (assistant_id WITH =, during WITH &&) WHERE (status IN ('booked','completed')),
  CONSTRAINT appt_no_unit_overlap      EXCLUDE USING gist (unit_id WITH =, during WITH &&)      WHERE (status IN ('booked','completed')),
  CONSTRAINT appt_no_patient_overlap   EXCLUDE USING gist (patient_id WITH =, during WITH &&)   WHERE (status IN ('booked','completed'))
);
CREATE UNIQUE INDEX appointments_plan_session_uq ON "tn-appointments" (plan_item_id, session_no)
  WHERE status IN ('booked','completed');
CREATE INDEX appointments_patient_idx ON "tn-appointments" (patient_id, lower(during) DESC);
CREATE INDEX appointments_start_idx ON "tn-appointments" (lower(during), id);

CREATE TABLE "tn-waitlist" (
  id             bigserial PRIMARY KEY,
  patient_id     bigint NOT NULL REFERENCES "tn-patients"(id),
  treatment_code text NOT NULL REFERENCES "tn-treatment_types"(code),
  dentist_id     bigint REFERENCES "tn-staff"(id),
  tooth          smallint CHECK (tooth IS NULL OR fdi_valid(tooth)),
  avail_window   tstzrange NOT NULL CHECK (NOT isempty(avail_window)),
  status         text NOT NULL DEFAULT 'waiting' CHECK (status IN ('waiting','booked','cancelled')),
  appointment_id bigint REFERENCES "tn-appointments"(id),
  created_at     timestamptz NOT NULL DEFAULT now(),
  CHECK ((status = 'booked') = (appointment_id IS NOT NULL))
);
CREATE INDEX waitlist_waiting_idx ON "tn-waitlist" (created_at, id) WHERE status = 'waiting';

-- Simüle SMS / hatırlatma olayları.
CREATE TABLE "tn-notifications" (
  id             bigserial PRIMARY KEY,
  patient_id     bigint NOT NULL REFERENCES "tn-patients"(id),
  appointment_id bigint REFERENCES "tn-appointments"(id),
  channel        text NOT NULL DEFAULT 'sms' CHECK (channel IN ('sms')),
  kind           text NOT NULL,
  body           text NOT NULL,
  created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notifications_patient_idx ON "tn-notifications" (patient_id, id DESC);

-- +goose Down
DROP TABLE "tn-notifications";
DROP TABLE "tn-waitlist";
DROP TABLE "tn-appointments";
DROP TABLE "tn-plan_items";
DROP TABLE "tn-plan_versions";
DROP TABLE "tn-treatment_plans";
DROP TABLE "tn-treatment_types";
DROP TABLE "tn-staff_leaves";
DROP TABLE "tn-units";
DROP TABLE "tn-staff";
DROP TABLE "tn-patients";
DROP TABLE "tn-institutions";
