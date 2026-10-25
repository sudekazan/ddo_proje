# TESTING — Hızlı Doğrulama Rehberi

## 0. Hazırlık

```bash
cp .env.example .env
docker compose up --build -d      # ~1,5 dk (soğuk makinede CI ölçümü: 83 sn)
make verify                       # tüm kimlikler için PASS/FAIL tablosu, hata varsa exit != 0
make reset-demo                   # verileri ilk seed haline döndürür (doğrulama tekrarlanabilir)
```

`make verify` ayaktaki sistemde çalışır. Kendi hastalarını ve randevularını oluşturur, simülatörün takvimine dokunmamak için 30–230 gün ilerideki günleri kullanır, bu yüzden arka arkaya tekrar çalıştırılabilir. Örnek çıktı (CI, Docker Compose):

```
KİMLİK  SONUÇ  SÜRE   KAPSAM
HEALTH  PASS   0s     GET /health veritabanı dahil sağlıklı
AUTH-1  PASS   135ms  Yanlış parola 401 döner
AUTH-2  PASS   816ms  Demo hesaplarla giriş (5 rol)
AUTH-3  PASS   1ms    Anahtarsız/bozuk anahtarlı istek 401
AUTH-4  PASS   142ms  Refresh token rotasyonu ve tekrar kullanım tespiti
RBAC-1  PASS   0s     Hasta, hasta listesine erişemez (403)
RBAC-2  PASS   4ms    Hasta başka hastanın kaydını/defterini göremez (403)
RBAC-3  PASS   2ms    Asistan ödeme alamaz, resepsiyon plan oluşturamaz, hekim fiyat değiştiremez (403)
F-1     PASS   22ms   Hasta kaydı: oluşturma, TC doğrulama, mükerrer kayıt, keyset arama
F-2     PASS   26ms   Randevu ve ünite: müsaitlik, otomatik kaynak atama, listeleme, iptal, ünite yönetimi
F-3     PASS   40ms   Diş haritası ve tedavi geçmişi: işlem sonrası durum ve zaman çizelgesi
F-4     PASS   44ms   Tedavi planı ve seanslar: plan, onay, seans randevusu, tamamlama
F-5     PASS   24ms   Ödeme ve taksit: peşinat + taksit, ödeme dağıtımı, cari hesap
F-6     PASS   8ms    Sarf malzeme stoğu: liste, lot girişi, hareketler
F-7     PASS   93ms   Raporlar: panel, gelir, hekim, ünite doluluğu, alacak, stok kullanımı
T-1     PASS   187ms  Çakışmasız randevu: 20 eşzamanlı istek, ünite çakışması, süre, izin, seans aralığı, bekleme listesi
T-2     PASS   45ms   Diş haritası/plan: FDI, çekilmiş dişe dolgu, kanal→kuron sıralama, döngü, sürüm ve onay
T-3     PASS   732ms  Fiyat/faturalama: zaman geçerli fiyat, kurum indirimi, ilerleme faturası, idempotans, iade, defter tutarlılığı
T-4     PASS   644ms  Stok: FEFO + SKT, minimum stok uyarısı, eşzamanlı tamamlama, geri almada iade
UI      PASS   2ms    Arayüz servis ediliyor ve API vekili çalışıyor

SONUÇ: 20/20 PASS
```

## (a) Demo hesaplar

Parola hepsi için **`Demo123!`**.

| Rol | E-posta | Notlar |
|---|---|---|
| Resepsiyon | `resepsiyon@dishekimim.local` | Hasta kaydı, randevu, iptal, bekleme listesi, izin, plan onayı (imzalı form) |
| Diş hekimi | `hekim@dishekimim.local` | Hekim no 1 (Dt. Hakan Ekinci). Plan oluşturma/revize, tamamlama, geri alma. Diğerleri: `hekim2` … `hekim6@dishekimim.local` |
| Asistan | `asistan@dishekimim.local` | Randevu tamamlama, stok girişi, reçete |
| Muhasebe | `muhasebe@dishekimim.local` | Ödeme, taksit, fiyat, plan iptali/iade, finans raporları |
| Hasta | `hasta@dishekimim.local` | Hasta no 1 (Ayşe Yılmaz): kendi randevuları, planı (onaylı demo planı: 36 kanal → kuron, 15 dolgu), cari hesabı, SMS'leri |
| Seed hesabı | `seed.orman@ddo-case.invalid` | Seed'in ilk kullanıcısı (resepsiyon yetkisi) |

## (b) Doğrulama kimlikleri — nasıl denenir / beklenen sonuç

İstek örneklerinin tamamı [requests.http](requests.http) içinde.

| Kimlik | Nasıl denenir | Beklenen sonuç |
|---|---|---|
| **HEALTH** | `GET /health` | `200 {"status":"ok","database":"ok",…}`; DB yoksa `503`. Ek uç `GET /_hz/6736` → 200. Her yanıtta `X-DDO-Node: 6736b1` |
| **AUTH** | `POST /api/auth/login` yanlış parola; `Authorization` olmadan `GET /api/patients`; `POST /api/auth/refresh` aynı refresh token ile iki kez | `401 UNAUTHORIZED`; `401`; ilk refresh 200 (yeni çift), eskisinin tekrarı 401 ve kullanıcının tüm oturumları kapanır |
| **RBAC** | Hasta: `GET /api/patients`, `GET /api/patients/2/ledger`. Asistan: `POST /api/payments`. Resepsiyon: `POST /api/plans`. Hekim: `POST /api/prices` | Hepsi `403 FORBIDDEN`; hasta kendi kaydını (`/api/patients/1`) görür |
| **F-1** Hasta kaydı | Resepsiyon `POST /api/patients` (geçerli TC); geçersiz TC; aynı TC tekrar; `GET /api/patients?q=yıl&limit=3` → `cursor=<next_cursor>` | `201`; `422 VALIDATION_ERROR` (`details.national_id`); `409 DUPLICATE_PATIENT`; keyset ikinci sayfa ilkini tekrarlamaz |
| **F-2** Randevu ve ünite | `GET /api/availability?date=…&treatment_code=EXAM`; `POST /api/appointments` (ünite/asistan vermeden); `GET /api/appointments?date=…`; `POST /api/appointments/{id}/cancel`; `POST /api/units`, `PATCH /api/units/{id} {"active":false}` | Slot listesi; `201` + otomatik `unit_id`/`assistant_id`; listede görünür; iptal `200`, ikinci iptal `409 NOT_CANCELLABLE`; pasif ünite atanmaz |
| **F-3** Diş haritası | Dolgu (diş 16) randevusu → asistan `POST /api/appointments/{id}/complete` → `GET /api/patients/{id}/teeth`, `GET …/teeth/16/events` | Haritada 32 kalıcı + 20 süt dişi, 16 `filled`; zaman çizelgesinde FILLING olayı (randevu id'siyle) |
| **F-4** Tedavi planı | Hekim `POST /api/plans`; onaysız plana seans; resepsiyon `POST /api/plans/{id}/approve {"version":1}`; `plan_item_id` ile randevu; tamamlama | Taslak plan; `409 PLAN_NOT_APPROVED`; onay; `session_no: 1`; kalem `done`, plan `in_progress` |
| **F-5** Ödeme ve taksit | Muhasebe `POST /api/plans/{id}/installments {"down_payment_kurus":100000,"count":3,…}`; `POST /api/payments` (Idempotency-Key) | Peşinat (sıra 0) + 3 taksit, toplam = plan neti; ödeme önce peşinata, kalanı 1. taksite dağıtılır; `GET /api/patients/{id}/account` bakiye günceller |
| **F-6** Stok | `GET /api/inventory/supplies`; asistan `POST /api/inventory/supplies/{id}/lots`; `GET /api/inventory/movements` | Stok miktarı lot kadar artar, `receive` hareketi oluşur |
| **F-7** Raporlar | `GET /api/reports/dashboard \| revenue \| dentists \| units \| receivables \| stock-usage` (rol uygun) | 200 + veri; gelir raporu son 12 ay için ≥ 6 ay döndürür; resepsiyon gelir raporuna `403` alır |
| **T-1** Randevu motoru | Aynı hekim + slot için 20 eşzamanlı `POST /api/appointments`; aynı saatte başka hekim + aynı `unit_id` / `assistant_id`; dolgu süresi; izin (`POST /api/staff/1/leaves`) sonrası rezervasyon; kanal planında 2. seans 3 gün / 7 gün sonra; bekleme listesi + iptal | Tam 1 × 201, 19 × `409 DENTIST_BUSY`; `409 UNIT_BUSY` / `409 ASSISTANT_BUSY`; bitiş − başlangıç = 45 dk; `409 DENTIST_ON_LEAVE`; `409 MIN_GAP_VIOLATION` / `201 session_no=2`; iptal yanıtında `waitlist_appointment` bekleyen hastaya aynı saatte |
| **T-2** Diş haritası/plan | Plan diş 19/60/86/9; diş 55; `POST …/teeth/26/observations {"state":"extracted"}` sonra 26'ya dolgu planı ve randevusu; kalemler `[CROWN 36, ROOT_CANAL 36]`; `depends_on` döngüsü; `POST /api/plans/{id}/revisions`, eski sürüm onayı; kanal planlanmadan kuron randevusu | `422 VALIDATION_ERROR`; 55 kabul; `422 INVALID_TOOTH_PROCEDURE` (plan ve randevu); kanal seq 1, kuron seq 2 ve `depends_on` = kanal; `422 DEPENDENCY_CYCLE`; v2 `draft`, v1 onayı `409 PLAN_VERSION_MISMATCH`, v2 onayı `signed_form`; `409 DEPENDENCY_NOT_MET` |
| **T-3** Fiyat/fatura/defter | Gelecek tarihli fiyat (`POST /api/prices`) → bugün ve o tarih için `GET /api/prices?date=`; kurumlu hastaya kanal planı; aynı Idempotency-Key ile ödeme ×2 ve farklı gövdeyle; 3 seans tamamlama; ödenmiş planı 1 seans sonra iptal; 10 eşzamanlı ödeme; `GET /api/billing/consistency` | Bugünkü fiyat değişmez, gelecekteki uygulanır; indirim = round(fiyat × %15); aynı ödeme id + `Idempotent-Replayed: true`, farklı gövde `422 IDEMPOTENCY_KEY_REUSED`; seans faturaları = `Split(net,3)`, toplam = net; iade = ödenen − faturalanan, bakiye 0; eşzamanlı ödemeler sonrası bakiye kuruşu kuruşuna doğru; `ok: true` |
| **T-4** Stok | Malzeme + lotlar (SKT geçmiş 10 g, yakın 1,5 g, uzak 2 g), reçete 1 g; tamamlama ×2; 5 eşzamanlı tamamlama; hekim `POST /api/appointments/{id}/revert` | 1.: yakın lottan düşer, geçmiş lota dokunulmaz; 2.: lotlar arası (0,5 + 0,5), minimum stok uyarısı açılır; eşzamanlıda tam 1 × 200, 4 × `409 INSUFFICIENT_STOCK`, hiçbir lot negatif değil; geri alma aynı lota 1 g iade eder, `charged_kurus < 0`, randevu `booked` |

## (c) Arayüzde elle denenecek senaryolar (http://localhost:3000)

1. **Canlı panel**: `resepsiyon@…` ile girin. Panel 4 sn'de bir yenilenir; simülatörün oluşturduğu randevular, iptaller, ödemeler ve "Çakışma engellendi" satırları akışta görünür.
2. **Hasta kaydı + randevu**: Hastalar → "+ Yeni hasta" (geçerli TC örn. `10000000146`) → hasta sayfası → Randevular → "+ Randevu al" → tedavi, tarih → "Müsait saatleri göster" → saate tıklayın. Aynı saati ikinci sekmeden almayı deneyin: `DENTIST_BUSY` hatası.
3. **Diş haritası**: `hekim@…` ile Hastalar → hasta 1 → Diş haritası. 26 numaralı diş dolgulu (mavi) görünür. Bir dişe tıklayın: zaman çizelgesi açılır. "Bulgu kaydet → Çekilmiş" sonrası dişte çarpı işareti görünür. "Süt dişleri" anahtarıyla 51–85.
4. **Tedavi planı**: Hekim → hasta → Tedavi planları → "+ Yeni tedavi planı" (varsayılan: 36 kanal + 36 kuron). Kaydedin: kanal 1., kuron 2. sırada ve bağımlı. "Revize et" ile yeni sürüm açın. `hasta@…` ile giriş → Bilgilerim → Tedavi planları → "Planı onayla".
5. **Seans + tamamlama + fatura**: Resepsiyon → hasta 1 → Tedavi planları → kanal kaleminde "Seans planla" → slot seçin. 2. seansı 7 günden önceye almayı deneyin: `MIN_GAP_VIOLATION`. `asistan@…` → Randevular → ilgili gün → "Tamamla". Hasta → Cari hesap: seans payı borç olarak görünür.
6. **Ödeme + taksit**: `muhasebe@…` → hasta → Tedavi planları → "Taksitlendir" (peşinat 1000 ₺, 3 taksit) → Cari hesap → "Ödeme al". Plan kartında taksitlerin ödenen sütunu dolar.
7. **Stok**: `asistan@…` → Stok → bir malzemede "Lotlar" (FEFO sırası, SKT'si geçmiş lot üstü çizili) → stok girişi. Minimum altındaki malzemeler satır renginde ve üst uyarıda görünür.
8. **Raporlar**: `muhasebe@…` → Raporlar: aylık gelir grafiği, hekim performansı, ünite doluluğu, alacaklar, stok kullanımı.
9. **Mobil**: tarayıcıyı 375 px genişliğe indirin. Menü hamburger menüye dönüşür, tablolar yatay kaydırılır.
10. **Yetki**: `hasta@…` ile adres çubuğuna `/patients` yazın: Bilgilerim'e yönlenir; API doğrudan 403 verir.

## Kanıt komutları (görev kimliğine göre)

| Kimlik | Kanıt komutu | Ne kanıtlar |
|---|---|---|
| **T-1** | `make verify` (T-1 satırı) | 20 eşzamanlı HTTP isteğinde 1 kazanan, ünite/asistan çakışması, süre, izin, seans aralığı, bekleme listesi |
| **T-1** | `make test-integration` → `TestConcurrentBookingSameSlotOnlyOneWins`, `TestConcurrentBookingDistributesSharedUnits`, `TestExclusionConstraintRejectsRawOverlap`, `TestLeaveBlocksBookingAndAvailability`, `TestWaitlistIsPromotedOnCancel` | 30 goroutine → 1 randevu; paylaşılan ünitelerin yeniden denemeyle dağıtılması; ham INSERT'in DB tarafından reddi |
| **T-1** | `make load` (k6) | 600 çekişmeli denemede `contention_created == 20`, `double_bookings == 0`, p95 < 500 ms eşikleri |
| **T-2** | `make verify` (T-2) · `cd backend && go test ./internal/dental/` · `TestExtractedToothRejectsFilling`, `TestPlanVersioningRequiresFreshApproval`, `TestMultiSessionPlanGapDependencyAndProgressBilling` | FDI sınırları, durum makinesi, kanal → kuron sıralaması, döngü tespiti, sürüm ve onay |
| **T-3** | `make verify` (T-3) · `cd backend && go test ./internal/billing/` (10.000 girdili `Split` özellik testi) · `TestConcurrentPaymentsKeepLedgerConsistent`, `TestPlanCancelRefundsOverpayment`, `TestHTTPIdempotentPaymentAndRBAC`, `TestAppendOnlyTablesRejectUpdates` | Kuruş yuvarlamasının toplamı bozmaması, eşzamanlı ödemede tutarlı defter, iade, idempotans, append-only |
| **T-4** | `make verify` (T-4) · `cd backend && go test ./internal/inventory/` · `TestConcurrentCompletionNeverOverdrawsStock`, `TestRevertReturnsStockToSameLotAndReversesCharge` | FEFO + SKT, eşzamanlı tamamlamada negatif stok olmaması, minimum uyarısı, aynı lota iade |
| Dizinler | `make explain` | Kritik sorguların dizinli/dizinsiz `EXPLAIN (ANALYZE, BUFFERS)` çıktısı |

Test sayıları: 30 birim + 14 entegrasyon (gerçek PostgreSQL 16) = 44 Go testi, artı `make verify` içinde 20 uçtan uca kontrol. CI hepsini her push'ta çalıştırır (`go test -race`, `golangci-lint`, Docker e2e).
