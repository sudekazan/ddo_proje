# Mimari Karar Kayıtları (ADR)

Her kayıt: **Bağlam → Seçenekler → Karar → Sonuç**.

---

## ADR-001 — Çifte rezervasyonu veritabanı exclusion constraint'iyle engellemek

**Bağlam.** Bir randevu hekim, ünite, asistan ve hastayı aynı zaman aralığında birlikte kullanır. Ünite ve asistan hekimler arasında paylaşılır, tedavi süreleri 15–90 dakika arasında değişir. Simülatör ve resepsiyon aynı slota eşzamanlı istek gönderebilir. Gereksinim: eşzamanlı çifte rezervasyon asla olmamalı ve bu bir testle kanıtlanmalı.

**Seçenekler.**
1. *Slot tablosu + `SELECT … FOR UPDATE SKIP LOCKED`.* Her kaynak için 15 dakikalık slot satırları önceden üretilir, randevu ilgili tüm slotları kilitler. Değişken süre bir randevunun N slotu atomik kilitlemesini gerektirir, üç kaynak türü için takvimin önceden üretilmesi ve izin ya da mesai değişikliklerinde güncel tutulması gerekir.
2. *`SERIALIZABLE` + yeniden deneme.* Doğru çalışır, ama çakışma sinyali yalnızca `40001`'dir: hangi kaynağın dolu olduğu kaybolur ve yoğunlukta yeniden deneme maliyeti artar. Garantinin doğruluğu uygulama sorgularının eksiksizliğine bağlıdır.
3. *Advisory lock (hekim, gün).* Paylaşılan ünite ve asistanı korumaz; birden çok kilit alınırsa sıralama ve kilitlenme riski doğar.
4. *`EXCLUDE USING gist (kaynak WITH =, during WITH &&) WHERE status IN ('booked','completed')`* ve ek olarak hekim satırı kilidi.

**Karar.** Seçenek 4. `tstzrange` `[)` aralıkları ve `btree_gist` ile dört kısmi exclusion constraint: hekim, asistan, ünite ve hasta. Rezervasyon transaction'ı önce hekim satırını `FOR UPDATE` ile kilitler. Böylece aynı hekime gelen istekler sıraya girer, izin ve hekim-dolu kontrolleri yarışsız yapılır ve kullanıcıya anlamlı hata (`DENTIST_BUSY`, `DENTIST_ON_LEAVE`) döner. Ünite ve asistan otomatik seçilir; paylaşılan kaynakta yarış kaybedilirse (exclusion ihlali) seçim 3 kez yeniden denenir. Kısıt adları HTTP hata kodlarına eşlenir (`appt_no_unit_overlap` → `409 UNIT_BUSY`).

**Sonuç.**
- (+) Garanti uygulamadan bağımsız. `TestExclusionConstraintRejectsRawOverlap` ham `INSERT`'in bile reddedildiğini gösteriyor. 30 eşzamanlı istekte tam 1 başarı (test), k6'da 600 çekişmeli denemede slot başına tam 1 kazanan.
- (+) İptal edilen ve gelmeyen (`no_show`) randevular kısıt dışında; slot yeniden kullanılabilir. Bitişik randevular (`[09:00,09:30)` ve `[09:30,…)`) çakışmaz.
- (+) Aynı GiST dizini çakışma sorgusunu da hızlandırıyor (3,38 ms → 0,037 ms).
- (−) Hekim satır kilidi tek hekim için bir serileşme noktası. Ölçülen yükte sorun değil (p95 < 20 ms), ama çok yüksek hacimde hekim+gün granülerliğine inmek gerekebilir.
- (−) Mesai saatleri ve 15 dakikalık grid uygulama tarafında; veritabanı yalnızca örtüşmeyi garanti ediyor.

---

## ADR-002 — Para: tam sayı kuruş, en büyük kalan yöntemi, append-only defter + kilitli bakiye

**Bağlam.** Fiyatlar zamana göre değişiyor, kurum indirimi yüzdesel; çok seanslı kalemler seans seans faturalanıyor, planlar taksitlendiriliyor, iptalde iade var. Gereksinim: `float` yasak, kuruş yuvarlaması toplamı bozmamalı, defter her hareketle tutarlı kalmalı.

**Seçenekler.**
- Tutar tipi: `numeric(12,2)` ↔ `bigint` kuruş.
- Bölme: her seansa `round(net/n)` ↔ son seansa fark ↔ en büyük kalan yöntemi.
- Bakiye: her sorguda `SUM(ledger)` ↔ önbellek bakiye sütunu + satır kilidi ↔ `SERIALIZABLE`.

**Karar.**
- Tüm tutarlar `bigint` kuruş (Go'da `int64`). İndirim baz puanla (`discount_bp`, 1500 = %15) kalem başına yarım-yukarı tam sayı aritmetiğiyle hesaplanır.
- Seans payları ve taksitler `billing.Split` ile bölünür (en büyük kalan yöntemi; ilk parçalar +1 kuruş). Parçalar toplamı her zaman bütüne eşittir; 10.000 rastgele girdili özellik testiyle doğrulanır.
- `tn-ledger_entries` append-only: UPDATE/DELETE tetikleyiciyle yasak, düzeltme ters kayıtla (`charge_reversal`, `reverses_id UNIQUE`) yapılır. Her hareket `tn-patient_accounts` satırını `FOR UPDATE` ile kilitler, yeni bakiyeyi ve hareketin `balance_after` değerini yazar. CHECK kısıtları işaretin türle uyumunu zorlar (borç > 0, ödeme < 0).
- Plan, oluşturulduğu günün fiyatını ve indirim oranını `plan_versions` satırında dondurur; fiyat listesindeki gelecek değişiklik mevcut planı etkilemez.

**Sonuç.**
- (+) Bakiye sorgusu O(1); `balance_after` ile her an geçmiş bakiye görülebiliyor. `GET /api/billing/consistency` üç değişmezi tek sorguda denetliyor ve `make verify` ile entegrasyon testleri bunu her senaryodan sonra çalıştırıyor.
- (+) 50 eşzamanlı ödeme testinde tutarsızlık yok; satır kilidi hasta başına serileştiriyor, farklı hastalar paralel.
- (−) İndirim kalem başına yuvarlandığından toplam indirim, toplam üzerinden hesaplanana göre ±1 kuruş farklı olabilir (bilinçli seçim: kalem neti seans faturalarının tabanı).

---

## ADR-003 — Stok: lot bazlı FEFO, sabit sıralı satır kilitleri ve CHECK

**Bağlam.** İşlem tamamlanınca reçetedeki malzemeler stoktan düşmeli. SKT'si en yakın lot önce kullanılmalı, SKT'si geçmiş lot kullanılmamalı. Eşzamanlı tamamlamalar stoğu negatife düşürmemeli; yanlış tamamlama geri alınınca stok iade edilmeli.

**Seçenekler.**
1. Malzeme başına tek miktar sütunu + koşullu `UPDATE … WHERE qty >= $n`: basit, ama lot ve SKT takibi yok.
2. Lot satırlarını `FOR UPDATE` ile kilitleyip uygulamada FEFO tahsisi.
3. `SERIALIZABLE` ile okuma-yazma.

**Karar.** Seçenek 2. Tamamlama transaction'ı reçete satırlarını malzeme id sırasıyla dolaşır. Her malzeme için `qty_milli > 0 AND expires_on >= bugün` lotlarını `ORDER BY expires_on, id FOR UPDATE` ile kilitler; saf `AllocateFEFO` fonksiyonu ihtiyacı lotlara dağıtır (yetersizse hiç tahsis yapmaz). Her düşüm bir `stock_movements` satırı yazar (append-only). Geri alma bu satırları ters kayıtla aynı lotlara iade eder. `CHECK (qty_milli >= 0)` son savunma hattı. Miktarlar mili birim tam sayı.

**Sonuç.**
- (+) READ COMMITTED altında bekleyen transaction kilit açıldığında satırı yeniden değerlendirir (`qty > 0` artık sağlanmıyorsa satır düşer). 10 eşzamanlı tamamlamada stok 3 işleme yetiyorsa tam 3 başarı, 7 × `INSUFFICIENT_STOCK`; lot 0'da kalır, SKT'si geçmiş lota dokunulmaz.
- (+) Kilit sırası sabit (malzeme, SKT, id) olduğu için iki tamamlama arasında kilitlenme oluşmuyor.
- (+) Tamamlama diş durumu, stok, plan ve defteri aynı transaction'da yazdığından stok yetersizliği diğer adımları da geri alıyor.
- (−) Reçete tedavi türüne sabit; diş boyutuna göre değişen miktar yok. Süresi geçen lotlar otomatik imha (write-off) kaydına dönüşmüyor, yalnızca kullanım dışı kalıyor.

---

## ADR-004 — Tedavi planı sürümleri değişmez; onay sürüme bağlı

**Bağlam.** Plan revize edilebilmeli ve hasta onayı kaydedilmeli. Hangi sürümün hangi fiyatla onaylandığı sonradan değişmemeli.

**Seçenekler.** (a) Planı yerinde güncelleyip değişikliği denetim kaydına yazmak; (b) her revizyonu ayrı `plan_versions` satırı ve kendi `plan_items` kümesiyle saklamak.

**Karar.** (b). `treatment_plans.current_version` geçerli sürümü gösterir. Revizyon yeni sürüm açar ve planı `draft`'a döndürür. Onay `{version}` ister; güncel olmayan sürüme verilen onay `409 PLAN_VERSION_MISMATCH` alır. Onay yöntemi (`patient_portal` / `signed_form`), zamanı ve onaylayan kullanıcı sürüm satırına yazılır. Randevu yalnızca güncel sürümün kalemine bağlanabilir. Revizyon, plana ait randevulu ya da tamamlanmış seans varken engellenir.

**Sonuç.** (+) Hasta neyi, hangi fiyatla onayladı sorusunun cevabı her zaman veritabanında. (−) Başlamış planda kısmi revizyon yok (bilinen sınırlama).

---

## ADR-005 — Modüler monolit ve modüller arası tek transaction (workflow)

**Bağlam.** Randevu tamamlama dört alanı birden etkiliyor: diş haritası, stok, plan ilerlemesi ve cari hesap. Herhangi biri başarısız olursa hiçbiri yazılmamalı.

**Seçenekler.** Mikroservis + saga / olay kuyruğu ↔ tek süreçte modüler paketler + tek veritabanı transaction'ı.

**Karar.** Tek Go binary, alan başına paket. Her modül `pgx.Tx` alan fonksiyonlar sunar (`dental.ApplyProcedure`, `inventory.Consume`, `billing.Post`); `internal/workflow` bunları tek transaction'da çağırır. Ortak altyapı `platform.InTx` (`40001`/`40P01`'de yeniden deneme), `httpx` (hata biçimi, doğrulama), `audit` (aynı transaction'da denetim kaydı) ve `idempotency` (Idempotency-Key ara katmanı).

**Sonuç.** (+) Atomiklik ücretsiz; saga telafi mantığı yok, test edilmesi kolay. (+) Denetim kaydı işlemle aynı transaction'da yazıldığı için "işlem oldu ama denetimde yok" durumu oluşamaz. (−) Modüller aynı şemayı paylaşıyor; ileride servise bölmek için sınırların korunması gerekir.

---

## ADR-006 — Kimlik: kısa ömürlü JWT + veritabanında dönen refresh token

**Bağlam.** Beş rol, her uç noktada yetki kontrolü; arayüz uzun süre açık kalıyor.

**Seçenekler.** Sunucu taraflı oturum (her istekte DB) ↔ yalnız JWT (iptal edilemez) ↔ kısa JWT + dönen refresh token.

**Karar.** bcrypt (cost 11) parola özeti. 15 dakikalık HS256 access token (rol, `staff_id`, `patient_id` claim'leri). 7 günlük opak refresh token yalnızca SHA-256 özetiyle saklanır ve her kullanımda döner. Dönmüş bir anahtarın tekrar kullanımı çalınma sayılır ve kullanıcının tüm refresh token'ları iptal edilir. RBAC rota grubunda `auth.Require(roller…)`; hasta kapsamı (`CanSeePatient`) veri düzeyinde kontrol edilir (hasta başka hastanın kaydına erişirse 403). Bilinmeyen e-postada da sahte bcrypt karşılaştırması yapılır, böylece süre farkından hesap tahmini yapılamaz.

**Sonuç.** (+) İstek başına DB maliyeti yok, oturum iptali refresh düzeyinde mümkün. (−) Access token çıkıştan sonra en fazla 15 dakika geçerli.

---

## ADR-007 — Frontend: React + Vite + TypeScript, UI kütüphanesi yok

**Bağlam.** Beş rol için ekranlar, etkileşimli diş haritası, slot seçimi, plan editörü. Mobil genişlikte kullanılabilir olmalı; her ekranda yükleniyor, boş ve hata durumları ele alınmalı.

**Seçenekler.** HTMX (sunucu şablonları) ↔ React/Vue SPA ↔ hazır bileşen kütüphanesi (MUI vb.).

**Karar.** React 18 + Vite + TypeScript, yalnızca `react-router-dom`. Ortak `Async` bileşeni yükleniyor / hata (tekrar dene) / boş durumlarını tek yerde ele alır. Diş haritası saf SVG (klavyeyle de erişilebilir). Mobil düzende yan menü hamburger menüye dönüşür. Production derlemesi `nginx-unprivileged` üzerinde servis edilir; `/api` aynı origin'e proxy'lenir (CORS gerekmez).

**Sonuç.** (+) Küçük paket (gzip ~67 KB), az bağımlılık, tip güvenli API modelleri. (−) Kapsamlı UI bileşen testi yok; arayüzün çalıştığı `make verify` UI kontrolü ve TESTING.md'deki elle senaryolarla doğrulanıyor.

---

## ADR-008 — Tablo adlarında `tn-` öneki

**Bağlam.** Proje standardı tüm tablo adlarına `tn-` öneki ve kullanıcı tablosu için tam olarak `tn-dk-users` adını şart koşuyor.

**Karar.** Tire içeren adlar PostgreSQL'de tırnaklı tanımlayıcı gerektirir. Tüm SQL'de (`"tn-patients"`), COPY çağrılarında (`pgx.Identifier`) ve goose sürüm tablosunda (`"tn-goose_db_version"`) tutarlı biçimde tırnaklı kullanıldı. Dizin ve kısıt adları önek almadan, hata eşlemesinde kullanılabilmeleri için açıkça adlandırıldı (ör. `patients_national_id_key`).

**Sonuç.** (+) Standarda uygun. (−) Elle yazılan SQL'de tırnak unutulursa sözdizimi hatası olur; entegrasyon testleri ve `make verify` tüm sorgu yollarını çalıştırarak bunu yakalıyor.
