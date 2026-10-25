const money = new Intl.NumberFormat("tr-TR", { style: "currency", currency: "TRY" });
export const tl = (kurus: number | undefined) => money.format((kurus ?? 0) / 100);

export const dateTime = (s: string) =>
  new Date(s).toLocaleString("tr-TR", { day: "2-digit", month: "2-digit", year: "numeric", hour: "2-digit", minute: "2-digit" });
export const date = (s: string) => new Date(s).toLocaleDateString("tr-TR");
export const time = (s: string) => new Date(s).toLocaleTimeString("tr-TR", { hour: "2-digit", minute: "2-digit" });

// isoDay returns YYYY-MM-DD in local time.
export function isoDay(d: Date) {
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

export const qty = (milli: number, unit: string) =>
  `${(milli / 1000).toLocaleString("tr-TR", { maximumFractionDigits: 3 })} ${unit}`;

export const statusLabel: Record<string, string> = {
  booked: "Planlandı", completed: "Tamamlandı", cancelled: "İptal", no_show: "Gelmedi",
  draft: "Onay bekliyor", approved: "Onaylı", in_progress: "Devam ediyor",
  pending: "Bekliyor", done: "Tamamlandı", waiting: "Bekliyor", open: "Açık", paid: "Ödendi",
};

export const roleLabel: Record<string, string> = {
  patient: "Hasta", dentist: "Diş hekimi", assistant: "Asistan", reception: "Resepsiyon", accounting: "Muhasebe",
};

export const toothStateLabel: Record<string, string> = {
  healthy: "Sağlam", filled: "Dolgulu", root_canal: "Kanal tedavili", crowned: "Kuronlu", extracted: "Çekilmiş", implant: "İmplant",
};
