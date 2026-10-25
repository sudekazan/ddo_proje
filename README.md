# DişHekimim — Diş Kliniği Randevu ve Tedavi Planı Sistemi

**Geliştiren:** Sudenur Kazan · DDO Yazılım Ekibi alım projesi

Çok hekimli bir diş kliniğinin randevu, kaynak (hekim + ünite + asistan), FDI diş haritası, sürümlü tedavi planı, ilerleme faturalaması / taksit / cari hesap ve lot-SKT bazlı sarf malzeme süreçlerini yöneten sistem. **Go 1.24 + PostgreSQL 16 + React**, tek komutla Docker Compose üzerinde çalışır.

[![CI](https://github.com/sudekazan/ddo_proje/actions/workflows/ci.yml/badge.svg)](https://github.com/sudekazan/ddo_proje/actions/workflows/ci.yml)

| Belge | İçerik |
|---|---|
| [TESTING.md](TESTING.md) | Demo hesaplar, her doğrulama kimliği için “nasıl denenir / beklenen sonuç”, elle UI senaryoları, kanıt komutları |
| [DECISIONS.md](DECISIONS.md) | Mimari karar kayıtları (bağlam, seçenekler, karar, sonuç) |
| [requests.http](requests.http) | Tüm uç noktalar için VS Code REST Client koleksiyonu |
| `http://localhost:8080/docs` | OpenAPI (Swagger UI); ham belge: `/openapi.yaml` |

## Kurulum ve çalıştırma

```bash
git clone https://github.com/sudekazan/ddo_proje.git && cd ddo_proje
cp .env.example .env
docker compose up --build
```

| Servis | Adres |
|---|---|
| Arayüz | http://localhost:3000 |
| API | http://localhost:8080 (sağlık: `/health`, Swagger: `/docs`) |
| PostgreSQL | yalnızca compose ağında (`db:5432`), host'a açılmaz |

Migrasyonlar ve seed `seed` servisiyle otomatik çalışır; `api` bu servis başarıyla bitmeden başlamaz (`service_completed_successfully`), `simulator` ve `frontend` ise `api` sağlıklı olunca başlar (`service_healthy`). CI'daki soğuk makinede (boş Docker önbelleği) sistemin tamamı **83 saniyede** sağlıklı hale geldi.

| Komut | Ne yapar |
|---|---|
| `make verify` | Ayaktaki sistemde tüm özellikleri ve zor görevleri API (ve arayüz) üzerinden dener, PASS/FAIL tablosu basar, hata varsa sıfırdan farklı çıkış kodu döner (~5 sn) |
| `make reset-demo` | Tüm tabloları boşaltıp ilk seed haline döndürür |
| `make load` | k6 yük testi (randevu motoru) |
| `make explain` | Kritik sorguların dizinli/dizinsiz `EXPLAIN (ANALYZE)` çıktısı |
| `make test` / `make test-integration` | Birim testleri / gerçek PostgreSQL 16 ile entegrasyon testleri |

### Demo hesaplar (parola hepsi için `Demo123!`)

| Rol | E-posta |
|---|---|
| Resepsiyon | `resepsiyon@dishekimim.local` |
| Diş hekimi | `hekim@dishekimim.local` (diğer hekimler: `hekim2` … `hekim6@dishekimim.local`) |
| Asistan | `asistan@dishekimim.local` |
| Muhasebe | `muhasebe@dishekimim.local` |
| Hasta | `hasta@dishekimim.local` (hasta no 1, Ayşe Yılmaz; onaylı demo planı var) |
| Seed hesabı (resepsiyon yetkisi) | `seed.orman@ddo-case.invalid` |

## Mimari özet

```
┌──────────────┐  /api (nginx proxy)  ┌──────────────────────────── Go API (chi) ─────────────────────────────┐
│ React + Vite │ ───────────────────▶ │ middleware: request-id · JSON log · recover · CORS · timeout            │
│  (nginx,     │                      │             JWT auth · RBAC (rol başına) · Idempotency-Key              │
│  uid 101)    │                      │ modüller: clinic · scheduling · dental · billing · inventory · reports  │
└──────────────┘                      │ workflow: tamamla / geri al / plan iptali (modüller arası tek tx)        │
┌──────────────┐  HTTP (canlı trafik) └───────────────┬─────────────────────────────────────────────────────────┘
│ simulator    │ ─────────────────────────────────────┘ pgx pool
└──────────────┘                                 ┌──────────────▼──────────────┐
┌──────────────┐  goose migrasyon + COPY seed    │ PostgreSQL 16               │
│ seed (1 kez) │ ──────────────────────────────▶ │ EXCLUDE (gist) · CHECK · FK │
└──────────────┘                                 │ append-only tetikleyiciler  │
                                                 └─────────────────────────────┘
```

- **Modüler monolit**: her alan (`internal/clinic`, `scheduling`, `dental`, `billing`, `inventory`, `reports`) kendi SQL'ini ve HTTP uçlarını taşır. Birden çok modülü etkileyen işlemler (`internal/workflow`) tek bir transaction içinde ilgili modül fonksiyonlarını çağırır. Örneğin randevu tamamlama aynı anda diş durumunu, FEFO stok düşümünü, plan ilerlemesini ve cari hesap borcunu yazar.
- **Doğruluk veritabanında**: çifte rezervasyonu `EXCLUDE USING gist` kısıtları, negatif stoğu `CHECK (qty_milli >= 0)`, FDI numarasını `CHECK (fdi_valid(tooth))`, fiyat dönemi çakışmasını yine bir exclusion constraint engeller. Uygulama kontrolleri kullanıcıya anlamlı hata vermek için var; son savunma hattı veritabanı.
- **Tek imaj, dört komut**: `backend` imajı distroless/nonroot (33,9 MB) ve `api`, `sim` (seed / reset / live), `verify` ve `explain` binary'lerini içerir. Frontend `nginx-unprivileged` (48,5 MB) üzerinde çalışır.
- **Yapılandırma** tamamen ortam değişkenlerinden (`.env.example`). Loglar yapılandırılmış JSON; parola, token ve istek gövdesi loglanmaz. `context` zaman aşımı (varsayılan 15 sn) ve SIGTERM'de düzgün kapanış (graceful shutdown) var.
- **Proje standardı**: tüm tablo adları `tn-` önekli, kullanıcı tablosu `"tn-dk-users"` (goose sürüm tablosu dahil). Tire içerdikleri için SQL'de her zaman tırnakla yazılırlar. Tüm API yanıtları `X-DDO-Node: 6736b1` başlığı taşır; `/health`'e ek olarak `GET /_hz/6736` ucu 200 döner. Backend container adı `ddo-ahlat-api`.

### Teknoloji seçimleri ve gerekçeleri

| Katman | Seçim | Neden |
|---|---|---|
| HTTP | `chi` | Standart `net/http` uyumlu, alt router ve middleware zinciri rol bazlı yetkiyi okunur kılıyor |
| DB sürücüsü | `pgx/v5` (ORM yok) | `tstzrange`, `daterange`, `COPY`, `SAVEPOINT`, `FOR UPDATE SKIP LOCKED` gibi PostgreSQL özelliklerine doğrudan erişim; kilit ve sorgu planı kontrolü |
| Migrasyon | `goose` (gömülü SQL) | Up/Down SQL dosyaları, binary içinde gömülü; seed servisi açılışta uyguluyor |
| Kimlik | bcrypt + JWT (HS256, 15 dk) + dönen refresh token | Refresh token veritabanında hash'li tutulur; tekrar kullanımı hırsızlık sayılır ve kullanıcının tüm oturumları kapanır |
| Frontend | **React 18 + Vite + TypeScript**, UI kütüphanesi yok | Çok sayıda etkileşimli form/ekran (diş haritası, slot seçimi, plan editörü) için bileşen modeli uygun. TS, API tiplerini sabitliyor. Bağımlılık az (3 runtime paketi), gzip'li paket ~67 KB. Diş haritası saf SVG |
| Yük testi | k6 | Senaryo bazlı eşzamanlılık ve eşik (threshold) ile otomatik geçti/kaldı |

Ayrıntılı gerekçeler [DECISIONS.md](DECISIONS.md) içinde.

## Zorlu çekirdek görevler: nasıl çözüldü

**T-1 Kaynak çakışmasız randevu motoru** (`internal/scheduling`)
- Randevu hekim + ünite + asistanı birlikte kullanır. Ünite ve asistan verilmezse boş olanlar otomatik atanır; süre tedavi türünden gelir (muayene 15, dolgu 45, kanal 60, implant 90 dk). Çalışma saatleri Pzt–Cmt 09:00–18:00, 15 dakikalık dilimler.
- İki katmanlı garanti: (1) hekim satırı `SELECT … FOR UPDATE` ile kilitlenir, böylece aynı hekime gelen istekler sıraya girer ve anlamlı hata döner; (2) hekim, ünite, asistan ve hasta için `EXCLUDE USING gist (kaynak WITH =, during WITH &&) WHERE status IN ('booked','completed')` kısıtları. Uygulama kodu atlansa bile örtüşen iki aktif randevu yazılamaz. Paylaşılan ünite/asistan için yarış kaybedilirse otomatik atama 3 kez yeniden denenir.
- Hekim/asistan izinleri (`staff_leaves`, kendi içinde çakışmasız), çok seanslı tedavilerde seanslar arası asgari takvim günü (kanal 7, kuron 7, implant 60) ve iki yönlü kontrol: iptal edilen ara seans yeniden planlanırken sonraki seansa da uzaklık aranır.
- İptalde aynı transaction içinde bekleme listesi taranır (`FOR UPDATE SKIP LOCKED`, FIFO). Boşalan slota sığan ilk talep aynı hekim/ünite/asistanla `SAVEPOINT` içinde randevuya çevrilir; aday uymazsa (ör. hastanın o saatte başka randevusu varsa) sıradakine geçilir. Reddedilen çifte rezervasyon denemeleri panelde "engellenen çakışma" olarak sayılır.

**T-2 Diş haritası ve tedavi planı** (`internal/dental`)
- FDI doğrulaması hem Go'da hem `fdi_valid()` SQL fonksiyonuyla CHECK kısıtında (11–48, 51–85). Diş durumu makinesi: sağlam / dolgulu / kanal tedavili / kuronlu / çekilmiş / implant. Çekilmiş dişe dolgu, sağlam dişe implant, süt dişine implant reddedilir. Kontrol plan oluşturmada, randevu alırken ve tamamlamada (satır kilidiyle) yapılır.
- Her işlem `tooth_events` zaman çizelgesine yazılır (durum öncesi/sonrası, hekim, randevu). Geri alınan işlem silinmez, `reverted_at` ile işaretlenir.
- Plan kalemleri açık bağımlılıkla veya klinik kurallarla (aynı dişte kanal → kuron, çekim → implant → kuron) Kahn algoritmasıyla sıralanır. Döngü reddedilir; plan mevcut haritanın üzerinde simüle edilir (plan içinde çekilen dişe dolgu planlanamaz). Kuron, kanalın tüm seansları planlanmadan randevulanamaz ve kanal bitmeden tamamlanamaz.
- Plan **sürümlenir** (`plan_versions`, değişmez kayıtlar). Revizyon yeni sürüm açar ve planı tekrar onaya düşürür. Onay belirli bir sürüme verilir (eski sürüm onayı 409), yöntemi (hasta portalı / imzalı form), zamanı ve onaylayan kaydedilir.

**T-3 Fiyat, ilerleme faturalaması ve cari bakiye** (`internal/billing`, `internal/workflow`)
- Fiyat listesi `daterange` + exclusion constraint ile çakışmasız dönemlerden oluşur. Yeni fiyat açık dönemi kapatır. Plan, oluşturulduğu günün fiyatını sürüm kaydında saklar; gelecek fiyat değişikliği mevcut planları etkilemez.
- Anlaşmalı kurum indirimi baz puan (1500 = %15) olarak kalem başına yarım-yukarı yuvarlanır. Her seans tamamlandığında kalem netinin **en büyük kalan yöntemiyle** bölünmüş payı faturalanır; seans payları kuruşu kuruşuna kalem netine eşittir (10.000 rastgele girdiyle özellik testi).
- Peşinat (sıra 0) + N taksit aynı yöntemle bölünür; ödemeler açık taksitlere sırayla dağıtılır. Plan iptalinde gelecek seanslar ve açık taksitler iptal edilir, `ödenen − faturalanan` iade edilir.
- Cari hesap defteri **append-only** (UPDATE/DELETE tetikleyiciyle yasak). Her hareket hasta hesap satırını `FOR UPDATE` ile kilitler ve `balance_after` yazar; böylece eşzamanlı ödemelerde de bakiye her an hareketlerin toplamına eşit kalır. `GET /api/billing/consistency` üç değişmezi denetler (önbellek bakiye = toplam, her satırda koşan toplam, plan kalemi faturası = defter).
- Para her yerde `bigint` kuruş; `float` yok.

**T-4 Sarf malzeme stoğu** (`internal/inventory`)
- İşlem tipi başına reçete (dolgu → 0,5 g kompozit + 0,1 ml bonding + 1 anestezi + 1 eldiven …). Miktarlar mili birim tam sayı.
- Tamamlamada lotlar `expires_on >= bugün AND qty > 0` koşuluyla, SKT sırasına göre (FEFO) `FOR UPDATE` ile kilitlenir. İhtiyaç birden çok lota yayılabilir; SKT'si geçmiş lot atlanır. Yetersizse işlem hiçbir yan etki bırakmadan `409 INSUFFICIENT_STOCK` döner. Kilit sırası sabit (malzeme, SKT, id) olduğu için kilitlenme (deadlock) yok; `CHECK (qty_milli >= 0)` son savunma hattı.
- Kullanılabilir stok minimumun altına inince uyarı açılır, stok girişiyle kapanır (malzeme başına tek açık uyarı, kısmi unique index).
- Yanlış tamamlama geri alınınca tüketim hareketleri tek tek ters kayıtla **aynı lotlara** iade edilir; borç ters kaydı ve diş durumu da aynı transaction'da geri alınır.

## Veri üretici (simülatör) ve seed

- `seed` servisi (`/app/sim seed`) **6 hekim, 6 asistan, 6 ünite, 20.000 hasta (geçerli TC kimlik numaralı), 3 yıllık ~53.000 randevu, ~97.000 defter kaydı**, diş zaman çizelgesi, lotlar ve 14 günlük ileri tarihli randevular üretir. Veriler `COPY` ile tek transaction'da yüklendiği için süre ~7 saniye.
- Üretim tek bir PCG akışından gelir: **aynı `SEED` + aynı `SEED_ANCHOR` (tarih) → aynı veri** (`TestGenerateIsDeterministic`). `SEED_ANCHOR` boşsa bugünün tarihi kullanılır. bcrypt tuzu rastgele olduğu için yalnızca parola hash'leri her yüklemede farklıdır.
- `simulator` servisi (`/app/sim live`) API üzerinden sürekli trafik üretir: rastgele randevular, aynı slota **3 eşzamanlı rezervasyon denemesi** (biri kazanır, ikisi 409), iptaller (bekleme listesi devreye girer), bekleme listesi talepleri, ödemeler ve hatırlatma SMS'leri. Arayüzdeki **Panel** her 4 saniyede güncellenir ve bu hareketler canlı akışta görünür.

## Test, CI ve kalite

- **44 Go testi**: 30 birim + 14 gerçek PostgreSQL 16 entegrasyon testi (`internal/itest`; her test kendi geçici veritabanını oluşturup migrasyonları uygular). Yarış durumları testle kanıtlanıyor: 30 eşzamanlı rezervasyondan tam 1 başarılı, 10 eşzamanlı tamamlamada stok asla negatife düşmüyor, 50 eşzamanlı ödemede defter tutarlı.
- **`make verify`**: 20 uçtan uca kontrol (HEALTH, AUTH-1..4, RBAC-1..3, F-1..F-7, T-1..T-4, UI). CI'da Docker Compose üzerinde, ardından `make reset-demo` ve ikinci bir `verify` ile çalışır.
- **GitHub Actions**: build + `golangci-lint` + `go test -race` (PostgreSQL 16 servis konteyneriyle), frontend tip denetimi + build, Docker e2e (`docker compose up --wait` → `make verify` → `make reset-demo` → `make verify`).

### Yük testi (k6) — `make load`

`loadtest/booking.js` iki senaryoyu birlikte çalıştırır. `spread`: 20 VU, 40 sn boyunca rastgele boş slota rezervasyon. `contention`: 30 VU'nun her iterasyonda **aynı** hekim ve aynı slota saldırması (20 slot × 30 deneme = 600 istek). Yerel çalıştırma sonucu (Apple Silicon, tek API süreci, PostgreSQL 16):

```
✓ checks.........................: 100.00% 50545 out of 50545
✓ contention_created.............: 20        ← 600 çekişmeli denemeden tam 20 kazanan (slot başına 1)
✓ double_bookings................: 0
✓ http_req_failed................: 0.00%     (409'lar beklenen yanıt; 5xx yok)
✓ { name:book }..................: avg=10.75ms p(90)=14.97ms p(95)=19.9ms
✓ { name:availability }..........: avg=17.58ms p(90)=21.31ms p(95)=22.93ms
  bookings_created...............: 11036
  http_reqs......................: 50545   1258.97/s
```

## Doğrulama Soruları

### 1. En zorlandığın görev hangisiydi? Hangi alternatifleri düşündün, neden eledin?

En çok **T-1 (kaynak çakışmasız randevu motoru)** zorladı. Zorluk tek bir kaynağı kilitlemekte değildi. Bir randevu üç kaynağı (hekim, ünite, asistan) ve hastayı birlikte tutuyor; ünite ve asistan ise hekimler arasında paylaşılıyor. Düşündüğüm seçenekler:

| Seçenek | Neden elendi / seçildi |
|---|---|
| Önceden üretilmiş **slot tablosu** + `FOR UPDATE SKIP LOCKED` | Değişken süreler (15–90 dk) bir randevunun birden çok slotu atomik kilitlemesini gerektiriyor. Üç kaynak türü için slot sayısı çarpılıyor, takvimi önceden üretmek ve izin/çalışma saati değişikliklerini senkron tutmak gerekiyor. **Elendi.** |
| **SERIALIZABLE** + yeniden deneme | Doğru çalışır, ama yoğun saatlerde `40001` yeniden denemeleri artar ve hatanın *hangi* kaynaktan geldiği kaybolur (kullanıcıya "ünite dolu" diyemezsiniz). Garanti uygulama mantığının eksiksizliğine bağlı. **Elendi.** |
| `pg_advisory_xact_lock(hekim, gün)` | Hekim boyutunu serileştirir ama paylaşılan ünite ve asistanı korumaz; birden çok kilit alınırsa kilitlenme sırası derdi başlar. **Elendi.** |
| Uygulama içi mutex | Birden çok API örneğinde çalışmaz. **Elendi.** |
| **`EXCLUDE USING gist` + hekim satır kilidi** | Garanti veritabanında, tüm kaynaklar için ve kısmi (yalnızca aktif randevular). Kilit, aynı hekime gelen istekleri anlamlı hatayla sıraya sokuyor; paylaşılan kaynaktaki yarışta kaybeden otomatik atama yeniden deniyor. **Seçildi** (ADR-001). |

İkinci zor nokta, iptalde bekleme listesinin aynı transaction içinde ve eşzamanlı iptallerde aynı talebi iki kez kullanmadan işlenmesiydi. `FOR UPDATE SKIP LOCKED` ve her aday için `SAVEPOINT` ile çözdüm.

### 2. Çözümünün kırıldığı ya da yetersiz kaldığı bir senaryo (bilinen sınırlamalar)

- **Bekleme listesi yalnızca birebir slot devrediyor.** İptal edilen 60 dakikalık bir slota 15 dakikalık bir talep sığarsa slotun başına yerleşiyor; kalan 45 dakika ikinci bir adaya verilmiyor. Ayrıca yalnızca aynı hekime (veya hekim tercihi olmayanlara) teklif ediliyor. Plan iptalinin topluca iptal ettiği randevular bekleme listesini tetiklemiyor.
- **Mesai saatleri sabit**: tüm hekimler için Pzt–Cmt 09:00–18:00, tek saat dilimi (Europe/Istanbul). Hekim bazlı çalışma takvimi yok; yalnızca izinle istisna tanımlanabiliyor. İzin, mevcut randevuların üzerine kaydedilirse çakışan randevular yanıtta listeleniyor ama otomatik ertelenmiyor.
- **Tamamlama zamanı denetlenmiyor**: ileri tarihli bir randevu bugün "tamamlandı" yapılabiliyor (uçtan uca doğrulamanın gelecekteki seansları tamamlayabilmesi için bilinçli bir gevşetme). Gerçek kullanımda `start - 1 saat` gibi bir alt sınır eklenmeli.
- **Plan revizyonu**, planın herhangi bir seansı randevulandıktan sonra engelleniyor; başlamış planda "kalan kalemleri değiştir" akışı yok, plan iptal edilip yenisi açılmalı.
- **İndirim kalem başına yuvarlanıyor**: 3 kalemli bir planda toplam indirim, toplam tutara tek seferde uygulanan indirimden ±1 kuruş farklı olabilir. Bu tutarlı ve belgelenmiş bir seçim (kalem neti seans faturalarının tabanı).
- **Idempotency anahtarları süresiz saklanıyor** (TTL temizliği yok). Access token iptal edilemiyor; çıkıştan sonra en fazla 15 dakika geçerli. Hız sınırlama (rate limit) yok.
- **Ölçek**: hekim satır kilidi, tek hekime saniyede yüzlerce rezervasyon gelirse o hekim için serileşme noktası olur (k6'da 30 VU'luk çekişmede p95 < 20 ms, sorun görülmedi).

### 3. Dizinlerden ikisi: hangi sorgu için, dizinsiz/dizinli fark

Ölçümler seed edilmiş veride (`tn-ledger_entries` ~97 bin, `tn-patients` 20 bin satır) `make explain` ile alındı. Araç dizini geri alınan bir transaction içinde düşürerek aynı sorguyu iki kez ölçüyor.

**a) `ledger_patient_idx ON "tn-ledger_entries" (patient_id, id DESC)`.** Hasta cari hesap sayfası (`GET /api/patients/{id}/ledger`, keyset `id < cursor ORDER BY id DESC`) ve ödeme sırasında defter okuma.

```
Dizinli:   Bitmap Index Scan on ledger_patient_idx  (rows=2)   Buffers: shared hit=4      Execution Time: 0.006 ms
Dizinsiz:  Seq Scan on "tn-ledger_entries"  Rows Removed by Filter: 97252  Buffers: shared hit=1256   Execution Time: 2.975 ms
```
Yaklaşık **500 kat** hızlanma; okunan sayfa sayısı 1256 → 4. Defter büyüdükçe dizinsiz maliyet doğrusal artıyor, dizinli sabit kalıyor.

**b) `patients_last_name_idx ON "tn-patients" (lower(last_name) text_pattern_ops, id)`.** Resepsiyonun soyad önekiyle hasta araması (`?q=yıl`, keyset `(lower(last_name), id) > (…)`).

```
Dizinli:   Bitmap Index Scan on patients_last_name_idx  Index Cond: (lower(last_name) ~>=~ 'yıl' AND ~<~ 'yım')   Execution Time: 0.570 ms
Dizinsiz:  Seq Scan on "tn-patients"  Rows Removed by Filter: 19744   Execution Time: 2.100 ms
```
`text_pattern_ops` sayesinde `LIKE 'önek%'` aralık taramasına dönüşüyor (~**3,7 kat**). Fark küçük görünse de tablo 20 bin satırla küçük; arama her tuş vuruşunda çalışıyor.

Ek olarak `appt_no_dentist_overlap` GiST dizini (exclusion constraint) randevu motorunun hekim çakışma kontrolünde 3,38 ms'den 0,037 ms'ye indiriyor (Index Only Scan). Tam çıktılar için `make explain`.

### 4. Yapay zekâ kullanımı: reddettiğim / düzelttiğim çıktılar

Projede yapay zekâ asistanı (Claude Code) kod, SQL, test ve doküman üretiminde yoğun kullanıldı. Ürettiği her şeyi testlerle doğruladım; testlerin yakaladığı hatalı çıktılardan ikisi:

**a) Eşzamanlılık testinin yanlış pozitif vermesi.** İlk üretilen entegrasyon testi yardımcısı, hataları yalnızca `*httpx.Error` tipine bakarak sınıflandırıyordu:

```go
// ÖNCE — veritabanından ham gelen exclusion hatasını (23P01) tanımıyordu
func errCode(err error) string {
	var he *httpx.Error
	if errors.As(err, &he) { return he.Code }
	return ""          // ← tanınmayan HER hata "" → test bunu "başarılı" sayıyordu
}
```
Test `ok=30 busy=0, want 1/29` ile kırıldı. Motor ham PostgreSQL hatası döndürüyor; çeviri yalnızca HTTP katmanında yapılıyordu. Yardımcı yanlış sınıflandırdığı için test, gerçekte veritabanının reddettiği 29 isteği başarılı saymıştı. Bir kanıt testinin kendisinin yanlış olması en tehlikeli hata türü. Düzeltmede `nil` dışındaki tanınmayan hatayı açıkça işaretledim:

```go
// SONRA
func errCode(err error) string {
	if err == nil { return "" }
	if he := httpx.FromDB(err); he != nil { return he.Code }   // 23P01 → DENTIST_BUSY
	var he *httpx.Error
	if errors.As(err, &he) { return he.Code }
	return "UNMAPPED: " + err.Error()                            // asla "başarı" sayılmaz
}
```
Aynı inceleme sırasında motorda da bir iyileştirme yaptım: hekim doluyken bile önce ünite seçilip "boş ünite yok" dönüyordu. Hekim çakışmasını satır kilidinden hemen sonra açıkça kontrol ettim; artık doğru hata (`DENTIST_BUSY`) dönüyor.

**b) Bekleme listesi sorgusunda tip çıkarımı.** Üretilen SQL'de parametreye aralık ekleniyordu:

```sql
-- ÖNCE: PostgreSQL $2'nin tipini "interval + interval"dan interval sanıyor → 500 hatası
AND w.avail_window @> tstzrange($2, $2 + make_interval(mins => t.duration_min))
-- SONRA
AND w.avail_window @> tstzrange($2::timestamptz, $2::timestamptz + make_interval(mins => t.duration_min))
```
Bu hata yalnızca iptal + bekleme listesi yolunda ortaya çıktığı için birim testlerinden geçmişti; `make verify` F-2 ve T-1'de 500 olarak yakaladı. Ayrıca kod reddedilen 500'lerin JSON yerine düz metin loglandığını da gösterdi; `slog.SetDefault` ile düzelttim.

Diğer düzeltmeler: determinizm testinin işaretçi adreslerini özetlemesi (her çalıştırmada farklı çıkıyordu → JSON özete geçildi), demo hastanın geçmiş randevularını mesai dışına (07:00) koyan üretici kodu (gün döngüsünün ilk randevusu yapıldı) ve `misspell` linter'ının veritabanı değeri olan `cancelled` kelimesini hata sayması (linter kaldırıldı).

### 5. "Kaynak çakışmasız randevu motoru"nun doğru çalıştığını nasıl kanıtladın?

Üç bağımsız kanıt var:

**(a) Entegrasyon testi, gerçek PostgreSQL 16, `-race` ile CI'da:**
```bash
make test-integration   # veya: TEST_DATABASE_URL=… go test ./internal/itest -run 'TestConcurrentBooking|TestExclusion' -v
```
```
=== RUN   TestConcurrentBookingSameSlotOnlyOneWins        # 30 goroutine, aynı hekim+slot → 1 başarılı, 29 DENTIST_BUSY, DB'de 1 aktif randevu
--- PASS: TestConcurrentBookingSameSlotOnlyOneWins (0.60s)
=== RUN   TestConcurrentBookingDistributesSharedUnits     # 3 hekim aynı anda, 3 ortak ünite → üçü farklı üniteye yerleşir
--- PASS: TestConcurrentBookingDistributesSharedUnits (0.30s)
=== RUN   TestExclusionConstraintRejectsRawOverlap        # uygulamayı atlayan ham INSERT bile reddedilir; bitişik [) aralık kabul
--- PASS: TestExclusionConstraintRejectsRawOverlap (0.28s)
PASS
ok  	github.com/sudekazan/ddo_proje/backend/internal/itest	1.769s
```

**(b) Ayaktaki sistemde uçtan uca:** `make verify` → T-1 satırı. 20 eşzamanlı HTTP isteği (tam 1 × 201, 19 × 409 `DENTIST_BUSY`, listede tek aktif randevu), aynı saatte başka hekime aynı ünite (`UNIT_BUSY`) ve aynı asistan (`ASSISTANT_BUSY`), tedavi süresi (dolgu 45 dk), izinli hekime randevu (`DENTIST_ON_LEAVE`), kanal 2. seansı 3 gün sonra (`MIN_GAP_VIOLATION`) / 7 gün sonra (201, seans 2), iptalde bekleme listesinden otomatik randevu:
```
T-1     PASS   187ms  Çakışmasız randevu: 20 eşzamanlı istek, ünite çakışması, süre, izin, seans aralığı, bekleme listesi
SONUÇ: 20/20 PASS
```

**(c) Yük altında:** `make load` → 600 çekişmeli denemede `contention_created = 20` (slot başına tam 1), `double_bookings = 0`, `http_req_failed = 0.00%` (yukarıdaki k6 bölümü).

## Depo yapısı

```
backend/
  cmd/api        HTTP API (serve | migrate | healthcheck)
  cmd/sim        seed | reset | live (veri üretici + simülatör)
  cmd/verify     make verify uçtan uca doğrulama
  cmd/explain    EXPLAIN (ANALYZE) dizinli/dizinsiz ölçüm
  internal/      platform · httpx · auth · audit · idempotency · clinic · scheduling · dental · billing · inventory · workflow · reports · seed · sim · apiclient · itest
  migrations/    goose SQL migrasyonları (gömülü)
frontend/        React + Vite + TS, nginx-unprivileged
loadtest/        k6 betiği
```

DDO Case Kodu: ORMAN-6736b1
