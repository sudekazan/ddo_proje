-- +goose Up
-- Zaman geçerlilikli fiyat listesi: aynı işlem için geçerlilik aralıkları çakışamaz.
CREATE TABLE price_list (
  id             bigserial PRIMARY KEY,
  treatment_code text NOT NULL REFERENCES treatment_types(code),
  price_kurus    bigint NOT NULL CHECK (price_kurus > 0),
  valid_during   daterange NOT NULL CHECK (NOT isempty(valid_during)),
  CONSTRAINT price_list_no_overlap EXCLUDE USING gist (treatment_code WITH =, valid_during WITH &&)
);

CREATE TABLE patient_accounts (
  patient_id    bigint PRIMARY KEY REFERENCES patients(id),
  balance_kurus bigint NOT NULL DEFAULT 0, -- pozitif: hasta borçlu
  updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE payments (
  id           bigserial PRIMARY KEY,
  patient_id   bigint NOT NULL REFERENCES patients(id),
  plan_id      bigint REFERENCES treatment_plans(id),
  amount_kurus bigint NOT NULL CHECK (amount_kurus > 0),
  method       text NOT NULL CHECK (method IN ('cash','card','transfer')),
  received_at  timestamptz NOT NULL DEFAULT now(),
  created_by   bigint REFERENCES users(id)
);
CREATE INDEX payments_plan_idx ON payments (plan_id) WHERE plan_id IS NOT NULL;
CREATE INDEX payments_received_idx ON payments (received_at);

CREATE TABLE refunds (
  id           bigserial PRIMARY KEY,
  patient_id   bigint NOT NULL REFERENCES patients(id),
  plan_id      bigint NOT NULL REFERENCES treatment_plans(id),
  amount_kurus bigint NOT NULL CHECK (amount_kurus > 0),
  created_at   timestamptz NOT NULL DEFAULT now(),
  created_by   bigint REFERENCES users(id)
);

-- Cari hesap defteri (append-only). amount_kurus işaretlidir: + borç artırır, - azaltır.
CREATE TABLE ledger_entries (
  id             bigserial PRIMARY KEY,
  patient_id     bigint NOT NULL REFERENCES patients(id),
  kind           text NOT NULL CHECK (kind IN ('charge','payment','refund','charge_reversal')),
  amount_kurus   bigint NOT NULL,
  balance_after  bigint NOT NULL,
  plan_id        bigint REFERENCES treatment_plans(id),
  plan_item_id   bigint REFERENCES plan_items(id),
  appointment_id bigint REFERENCES appointments(id),
  payment_id     bigint REFERENCES payments(id),
  refund_id      bigint REFERENCES refunds(id),
  reverses_id    bigint UNIQUE REFERENCES ledger_entries(id),
  description    text NOT NULL,
  created_by     bigint REFERENCES users(id),
  created_at     timestamptz NOT NULL DEFAULT now(),
  CHECK ((kind IN ('charge','refund') AND amount_kurus > 0)
      OR (kind IN ('payment','charge_reversal') AND amount_kurus < 0)),
  CHECK ((kind = 'charge_reversal') = (reverses_id IS NOT NULL))
);
CREATE INDEX ledger_patient_idx ON ledger_entries (patient_id, id DESC);
CREATE INDEX ledger_appointment_idx ON ledger_entries (appointment_id) WHERE appointment_id IS NOT NULL;
CREATE INDEX ledger_created_idx ON ledger_entries (created_at);
CREATE TRIGGER ledger_append_only BEFORE UPDATE OR DELETE ON ledger_entries
  FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

CREATE TABLE installments (
  id           bigserial PRIMARY KEY,
  plan_id      bigint NOT NULL REFERENCES treatment_plans(id),
  seq          int NOT NULL CHECK (seq >= 0), -- 0 = peşinat
  due_date     date NOT NULL,
  amount_kurus bigint NOT NULL CHECK (amount_kurus > 0),
  paid_kurus   bigint NOT NULL DEFAULT 0 CHECK (paid_kurus BETWEEN 0 AND amount_kurus),
  status       text NOT NULL DEFAULT 'open' CHECK (status IN ('open','paid','cancelled')),
  UNIQUE (plan_id, seq)
);
CREATE INDEX installments_due_idx ON installments (due_date) WHERE status = 'open';

-- +goose Down
DROP TABLE installments;
DROP TABLE ledger_entries;
DROP TABLE refunds;
DROP TABLE payments;
DROP TABLE patient_accounts;
DROP TABLE price_list;
