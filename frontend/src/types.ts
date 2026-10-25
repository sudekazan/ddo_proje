export interface Patient {
  id: number; national_id: string; first_name: string; last_name: string; birth_date: string; phone: string;
  email: string | null; institution_id: number | null; institution: string | null; discount_bp: number; balance_kurus: number;
}
export interface Page<T> { items: T[]; next_cursor?: string }
export interface Appointment {
  id: number; patient_id: number; patient_name: string; dentist_id: number; dentist_name: string; assistant_name: string;
  unit_id: number; unit_name: string; treatment_code: string; treatment_name: string; tooth: number | null;
  plan_item_id: number | null; session_no: number | null; start: string; end: string; status: string; cancel_reason: string | null;
}
export interface Staff { id: number; full_name: string; kind: string; active: boolean }
export interface TreatmentType { code: string; name: string; duration_min: number; sessions: number; min_gap_days: number; requires_tooth: boolean }
export interface PlanItem {
  id: number; seq: number; tooth: number | null; treatment_code: string; treatment_name: string; depends_on: number | null;
  sessions_total: number; sessions_done: number; unit_price_kurus: number; discount_kurus: number; net_kurus: number; billed_kurus: number; status: string;
}
export interface PlanVersion {
  version: number; price_date: string; discount_bp: number; gross_kurus: number; discount_kurus: number; net_kurus: number;
  note: string; created_at: string; approved_at: string | null; approval_method: string | null;
}
export interface Plan {
  id: number; patient_id: number; patient_name: string; dentist_name: string; status: string; current_version: number;
  created_at: string; versions: PlanVersion[]; items: PlanItem[];
}
export interface Slot { dentist_id: number; dentist_name: string; start: string; end: string; unit_id: number; assistant_id: number }
