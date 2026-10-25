import { Link } from "react-router-dom";
import { useApi } from "../hooks";
import { Async, Card, Stat } from "../components/ui";
import { dateTime, statusLabel, time } from "../format";

interface Dashboard {
  server_time: string;
  today_by_status: Record<string, number>;
  upcoming_booked: number;
  booked_last_hour: number;
  waitlist_waiting: number;
  open_stock_alerts: number;
  total_patients: number;
  conflicts_prevented_last_hour: number;
  recent_activity: { id: number; at: string; action: string; entity: string; entity_id: string; actor: string | null }[];
}

const actionLabel: Record<string, string> = {
  "appointment.book": "Randevu oluşturuldu", "appointment.cancel": "Randevu iptal edildi", "appointment.complete": "İşlem tamamlandı",
  "appointment.revert": "Tamamlama geri alındı", "waitlist.add": "Bekleme listesine eklendi", "waitlist.promote": "Bekleme listesinden randevu",
  "payment.create": "Ödeme alındı", "plan.create": "Tedavi planı oluşturuldu", "plan.approve": "Plan onaylandı", "plan.revise": "Plan revize edildi",
  "plan.cancel": "Plan iptal edildi", "patient.create": "Hasta kaydı", "stock.receive": "Stok girişi", "booking.conflict": "Çakışma engellendi",
  "installments.create": "Taksit planı", "price.create": "Fiyat tanımlandı", "seed.load": "Seed yüklendi",
};

// Polled every 4 s so the simulator's traffic is visible: the system is live.
export function DashboardPage() {
  const s = useApi<Dashboard>("/api/reports/dashboard", 4000);
  return (
    <Async state={s}>
      {(d) => {
        const today = Object.values(d.today_by_status).reduce((a, b) => a + b, 0);
        return (
          <>
            <div className="live"><span className="pulse" /> Canlı · son güncelleme {time(d.server_time)}</div>
            <div className="stats">
              <Stat label="Bugünkü randevu" value={today} hint={Object.entries(d.today_by_status).map(([k, v]) => `${statusLabel[k] ?? k}: ${v}`).join(" · ")} />
              <Stat label="Gelecek randevular" value={d.upcoming_booked} />
              <Stat label="Son 1 saatte oluşturulan" value={d.booked_last_hour} />
              <Stat label="Engellenen çifte rezervasyon (1 sa)" value={d.conflicts_prevented_last_hour} />
              <Stat label="Bekleme listesi" value={d.waitlist_waiting} />
              <Stat label="Stok uyarısı" value={d.open_stock_alerts} hint={d.open_stock_alerts ? "Stok sayfasına bakın" : "Sorun yok"} />
              <Stat label="Kayıtlı hasta" value={d.total_patients.toLocaleString("tr-TR")} />
            </div>
            <Card title="Son hareketler" actions={<Link to="/appointments">Randevulara git →</Link>}>
              <ul className="feed">
                {d.recent_activity.map((a) => (
                  <li key={a.id} className={a.action === "booking.conflict" ? "warn" : ""}>
                    <span className="when">{dateTime(a.at)}</span>
                    <span>{actionLabel[a.action] ?? a.action}</span>
                    <span className="muted">{a.entity} #{a.entity_id}{a.actor ? ` · ${a.actor}` : ""}</span>
                  </li>
                ))}
              </ul>
            </Card>
          </>
        );
      }}
    </Async>
  );
}
