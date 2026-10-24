-- +goose Up
-- Miktarlar "mili birim" (1/1000) cinsinden tam sayı tutulur: 0,5 g = 500.
CREATE TABLE "tn-supplies" (
  id             bigserial PRIMARY KEY,
  sku            text NOT NULL UNIQUE,
  name           text NOT NULL,
  unit           text NOT NULL CHECK (unit IN ('g','ml','adet')),
  min_stock_milli bigint NOT NULL DEFAULT 0 CHECK (min_stock_milli >= 0)
);

CREATE TABLE "tn-supply_lots" (
  id          bigserial PRIMARY KEY,
  supply_id   bigint NOT NULL REFERENCES "tn-supplies"(id),
  lot_no      text NOT NULL,
  expires_on  date NOT NULL,
  qty_milli   bigint NOT NULL CONSTRAINT supply_lots_qty_milli_check CHECK (qty_milli >= 0), -- stok asla negatife düşemez
  received_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (supply_id, lot_no)
);
CREATE INDEX supply_lots_fefo_idx ON "tn-supply_lots" (supply_id, expires_on, id) WHERE qty_milli > 0;

CREATE TABLE "tn-recipes" (
  treatment_code text NOT NULL REFERENCES "tn-treatment_types"(code),
  supply_id      bigint NOT NULL REFERENCES "tn-supplies"(id),
  qty_milli      bigint NOT NULL CHECK (qty_milli > 0),
  PRIMARY KEY (treatment_code, supply_id)
);

CREATE TABLE "tn-stock_movements" (
  id             bigserial PRIMARY KEY,
  supply_id      bigint NOT NULL REFERENCES "tn-supplies"(id),
  lot_id         bigint NOT NULL REFERENCES "tn-supply_lots"(id),
  appointment_id bigint REFERENCES "tn-appointments"(id),
  kind           text NOT NULL CHECK (kind IN ('receive','consume','return')),
  qty_milli      bigint NOT NULL CHECK (qty_milli <> 0), -- tüketim negatif
  reverses_id    bigint UNIQUE REFERENCES "tn-stock_movements"(id),
  created_by     bigint REFERENCES "tn-dk-users"(id),
  created_at     timestamptz NOT NULL DEFAULT now(),
  CHECK ((kind = 'consume') = (qty_milli < 0)),
  CHECK ((kind = 'return') = (reverses_id IS NOT NULL))
);
CREATE INDEX stock_movements_appointment_idx ON "tn-stock_movements" (appointment_id) WHERE appointment_id IS NOT NULL;
CREATE INDEX stock_movements_created_idx ON "tn-stock_movements" (created_at);
CREATE TRIGGER stock_movements_append_only BEFORE UPDATE OR DELETE ON "tn-stock_movements"
  FOR EACH ROW EXECUTE FUNCTION forbid_mutation();

CREATE TABLE "tn-stock_alerts" (
  id          bigserial PRIMARY KEY,
  supply_id   bigint NOT NULL REFERENCES "tn-supplies"(id),
  qty_milli   bigint NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz
);
CREATE UNIQUE INDEX stock_alerts_open_uq ON "tn-stock_alerts" (supply_id) WHERE resolved_at IS NULL;

-- +goose Down
DROP TABLE "tn-stock_alerts";
DROP TABLE "tn-stock_movements";
DROP TABLE "tn-recipes";
DROP TABLE "tn-supply_lots";
DROP TABLE "tn-supplies";
