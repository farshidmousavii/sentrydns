# SentryDNS — معماری و مستندات فنی / Architecture & Technical Documentation

<div dir="rtl" lang="fa">

---

## فهرست مطالب

- [معماری کلی](#معماری-کلی)
- [جریان درخواست به صورت گام‌به‌گام](#جریان-درخواست-به-صورت-گامبهگام)
- [جزئیات کامپوننت‌ها](#جزئیات-کامپوننتها)
- [مدل همزمانی (Concurrency Model)](#مدل-همزمانی-concurrency-model)
- [حالت‌های شکست (Failure Modes)](#حالتهای-شکست-failure-modes)
- [حساس‌ترین منطق‌ها — قبل از تغییر حتماً بخوانید](#حساسترین-منطقها--قبل-از-تغییر-حتماً-بخوانید)
- [تنظیمات کامل (Config Reference)](#تنظیمات-کامل-config-reference)
- [Metricها و مانیتورینگ](#metricها-و-مانیتورینگ)
- [نحوه استقرار](#نحوه-استقرار)
- [عیب‌یابی](#عیبیابی)

---

## معماری کلی

SentryDNS یک پروکسی تطبیقی DNS است که به طور خودکار یاد می‌گیرد کدام دامنه باید به سرور بالادستی ایران و کدام به سرور بالادستی جهانی هدایت شود.

```txt
 کلاینت (UDP/TCP :53)
      │
      ▼
┌──────────────┐
│   Handler    │  ← rate limit, panic recovery, timeout context
└──────┬───────┘
       │
┌──────▼───────┐
│  Resolver    │  ← 4 مسیر تصمیم‌گیری: TLD → preferIran → store → learn
│  + Cache     │
│  + Learning  │
└──────┬───────┘
       │
┌──────▼───────┐      ┌──────────────────┐      ┌──────────────────┐
│   query()    │─────►│  IranCB          │─────►│  GlobalCB        │
│  (upstream)  │      │  circuitBreaker  │      │  circuitBreaker  │
└──────┬───────┘      └──────────────────┘      └──────────────────┘
       │
       ├──► IranDNS  (172.16.0.25)
       └──► GlobalDNS (172.16.0.24)
               └── GlobalDNS Fallback (اختیاری)
```

### بسته‌ها (Packages)

| بسته                    | مسئولیت                                                  |
| ----------------------- | -------------------------------------------------------- |
| `cmd/sentrydns/main.go` | راه‌اندازی، سیم‌کشی کامپوننت‌ها، graceful shutdown       |
| `internal/config`       | بارگذاری YAML، اعتبارسنجی، مقادیر پیش‌فرض                |
| `internal/resolver`     | هسته اصلی: تصمیم‌گیری مسیریابی، یادگیری، circuit breaker |
| `internal/cache`        | کش DNS با TTL هوشمند، حداقل ۳۰۰s و حداکثر ۳۶۰۰s          |
| `internal/classifier`   | بارگذاری رنج‌های IP ایران، تابع IsIran()                 |
| `internal/store`        | ذخیره دامنه‌های یادگرفته شده در فایل                     |
| `internal/updater`      | به‌روزرسانی خودکار رنج‌های IP ایران                      |
| `internal/metrics`      | شمارنده‌های اتمیک، ریست روزانه، HTTP Endpoint            |
| `internal/state`        | persistence اتمیک JSON برای metadata                     |
| `internal/ratelimit`    | محدودکننده نرخ به ازای هر کلاینت + سقف QPS سراسری (token bucket) |
| `internal/logger`       | لاگ JSON/text ساختاریافته                                |

---

## جریان درخواست به صورت گام‌به‌گام

### 1. ورودی (Handler در main.go:121)

```go
dns.HandleFunc(".", func(w dns.ResponseWriter, req *dns.Msg) {
    // ۱. بازیابی از panic
    // ۲. بررسی سوال خالی → SERVFAIL
    // ۳. loop detection: اگر منبع کوئری یکی از بالادستی‌ها باشد → REFUSED
    // ۴. سقف QPS سراسری (global_qps_limit) → SERVFAIL
    // ۵. rate limit به ازای هر کلاینت → SERVFAIL
    // ۶. context.WithTimeout 10s
    // ۷. r.Resolve(ctx, req)
    // ۸. nil → SERVFAIL
    // ۹. w.WriteMsg(resp)
})
```

### 2. تصمیم‌گیری (Resolver.resolve, resolver.go:462)

```txt
resolve(ctx, req, domain)
  │
  ├─ isIranTLD(domain)?
  │     ├─ queryIranDNS() → success + non-hijacked → PathTLD++, return
  │     └─ fail/hijack → fallback GlobalDNS
  │
  ├─ isPreferIran(domain)?
  │     ├─ queryIranDNS() → success + non-hijacked + has IPs → PathPreferIran++, return
  │     └─ fail → fallback GlobalDNS
  │
  ├─ store.IsIran(domain)? (دامنه قبلاً یاد گرفته شده)
  │     ├─ queryIranDNS()
  │     ├─ NXDOMAIN? → Remove + re-learn (resolveWithLearning)
  │     ├─ nil/error → fallback GlobalDNS
  │     ├─ hijacked → fallback GlobalDNS
  │     └─ success → PathStore++, return
  │
  └─ unknown domain → resolveWithLearning() → PathLearn++
```

### 3. یادگیری همزمان (resolveWithLearning, resolver.go:246)

این پیچیده‌ترین تابع است:

```go
resolveWithLearning(ctx, req, domain)
  │
  ├─ تعیین نیاز به GlobalDNS (A/AAAA یا circuit breaker باز)
  │
  ├─ بررسی GlobalCB:
  │   ├─ اگر circuit breaker باز است → skip GlobalDNS (ثبت GlobalCBSkipped)
  │   └─ اگر بسته است → ادامه
  │
  ├─ راه‌اندازی goroutine‌های موازی:
  │   ├─ IranDNS → r.query(iranCtx, reqCopy, iranDNS)
  │   └─ GlobalDNS → r.query(globalCtx, reqCopy, globalDNS) (در صورت skip نشدن)
  │
  ├─ shortWait = globalTimeout/4 (حداقل ۱۰۰ms)
  │
  ├─ select اول: منتظر سریع‌ترین پاسخ
  │   ├─ ایران اول رسید → بررسی IPها
  │   │   ├─ IP ایرانی + معتبر → یادگیری (store.Add) + لغو GlobalDNS → return Iran
  │   │   ├─ IP غیرایرانی → منتظر shortWait دیگر برای GlobalDNS
  │   │   │   ├─ Global رسید → return Global
  │   │   │   └─ تایم‌اوت → WARN log + SERVFAIL
  │   │   └─ non-A/AAAA → بلافاصله return Iran (نیاز به تأیید IP نیست)
  │   │
  │   ├─ Global اول رسید → لغو Iran
  │   │   ├─ منتظر shortWait برای ایران (برای یادگیری، نه تغییر مسیر)
  │   │   └─ return Global
  │   │
  │   └─ context done → SERVFAIL
  │
  ├─ اگر GlobalCB باز بود و ایران nil → fallback سینکرون به GlobalDNS
  ├─ اگر هر دو nil بودند → fallback سینکرون به GlobalDNS
  │
  └─ هیچ‌کدام جواب نداد → WARN "no suitable upstream" + SERVFAIL
```

### 4. مدار شکن (resolver.go:47-104)

دو مدارشکن مستقل:

**IranCB** — سه حالت: `cbClosed` (0) ← `cbOpen` (1) ← `cbHalfOpen` (2)
- در حالت Open، تمام کوئری‌های IranDNS **رد می‌شوند** (Skipped)
- بعد از `cooldown` (پیش‌فرض ۳۰s)، یک Half-Open probe مجاز است
- Half-Open success → Closed; failure → Open
- `recordFailure()`: اگر Half-Open باشد یا threshold رسیده باشد → Open
- `recordSuccess()`: اگر Half-Open باشد → Closed; اگر Closed باشد → کاهش failures

**GlobalCB** — همان ساختار با ایزوله کامل:
- در حالت Open، گوروتین GlobalDNS در `resolveWithLearning` **راه‌اندازی نمی‌شود** (Skipped)
- تعداد skipها در `GlobalCBSkipped` ثبت می‌شود
- `query()` برای GlobalDNS success/failure را روی GlobalCB ثبت می‌کند
- آستانهٔ پیش‌فرض: ۱۰ خطا، cooldown: ۳۰s
- هدف: جلوگیری از سیل کوئری به GlobalDNS وقتی مشکل دارد (کاهش بار upstream)

### 5. تابع query (resolver.go:536-643)

```go
query(ctx, req, upstream)
  │
  ├─ بررسی context.Done()
  ├─ تعیین timeout (iran: 3s, global: 1.5-5s)
  ├─ رعایت deadline والد (هر کدام کوچک‌تر است)
  ├─ dns.Client از sync.Pool
   ├─ تنظیم EDNS0 با ۱۲۳۲ بایت
  ├─ randDNSID() (اتمیک)
  ├─ ExchangeContext()
  │
  ├─ ثبت metric و circuit breaker:
  │   ├─ Iran: IranQueryCount++, error→IranTimeouts+iranCb.recordFailure()
  │   └─ Global: GlobalQueryCount++, error→GlobalTimeouts+globalCb.recordFailure()
  │                           , success→globalCb.recordSuccess()
  │
  ├─ خطا + global fallback → تلاش دوم
  ├─ Truncated → TCP fallback
  └─ return resp
```

---

## جزئیات کامپوننت‌ها

### Resolver — جزئیات (internal/resolver/resolver.go)

**ساختار:**

```go
type Resolver struct {
    classifier        *classifier.Classifier
    store             *store.Store
    cache             *cache.Cache
    iranDNS, globalDNS, globalDNSFallback string
    timeout, globalTimeout atomic.Int64
    log               *slog.Logger
    metrics           *metrics.Metrics
    iranTLDs          map[string]bool
    hijackIPs         map[string]bool
    hijackRanges      []netip.Prefix
    preferIran        map[string]bool
    sf                singleflight.Group
    iranCb            *circuitBreaker   // مدارشکن ایران DNS
    globalCb          *circuitBreaker   // مدارشکن Global DNS
    iranAddr, globalAddr string
    stopped           atomic.Bool
    stopCh            chan struct{}
    active            atomic.Int64
}
```

**چهار مسیر تصمیم‌گیری به ترتیب اولویت:**

1. **TLD** (.ir, .ایران) — سریع‌ترین مسیر، بدون یادگیری
2. **PreferIran** — دامنه‌هایی که به طور دستی مشخص شده‌اند (google.com, easy4ipcloud.com)
3. **Store** — دامنه‌هایی که قبلاً یاد گرفته شده‌اند
4. **Learn** — دامنه ناشناخته → کوئری موازی + اعتبارسنجی + یادگیری

**Singleflight** (resolver.go:429): کوئری‌های همزمان برای domain:qtype یکسان را ادغام می‌کند. فقط یک goroutine واقعاً کوئری می‌زند.

**Retry** (resolver.go:432-438): اگر نتیجه nil یا SERVFAIL باشد، یک بار با jitter (`timeout/15 + random(timeout/30)`) دوباره تلاش می‌کند.

### Cache (internal/cache/cache.go)

- کلید: `domain:Qtype` (لوئرکیس)
- فقط پاسخ‌های موفق با Answer کش می‌شوند
- TTL به [minTTL, maxTTL] محدود می‌شود
- `Get()`: کپی برمی‌گرداند با اصلاح ID به req.Id
- `evictOne()`: وقتی به max entries رسید، ۱۰۰ تا نمونه می‌گیرد و قدیمی‌ترین را حذف می‌کند (approximate LRU)
- پاکسازی پس‌زمینه هر ۵ دقیقه

### Classifier (internal/classifier/classifier.go)

- بارگذاری رنج‌های CIDR از فایل
- جدا کردن IPv4/IPv6 و مرتب‌سازی نزولی (مشخص‌ترین اول)
- `IsIran(ip)`: جستجوی خطی O(n) روی ~۶۰۰ CIDR
- `Reload(path)`: تعویض اتمیک رنج‌ها (برای updater)

### Store (internal/store/store.go)

- دامنه‌ها در memory به صورت `map[string]bool`
- `persistBuf`: بافر ۵ ثانیه‌ای برای append به فایل
- `writeAll()`: بازنویسی کامل با temp → fsync → rename
- `Remove()`: debounce ۵۰۰ms برای rewrite
- `IsIran(domain)`: بررسی domain + parent domainها تا TLD
- `cleanup()`: با `qps` کارگر موازی، دامنه‌ها را اعتبارسنجی می‌کند (ValidateDomain)
- `StartCleanup()`: با تأخیر اولیه (پیش‌فرض ۱h) و برنامه زمانبندی (پیش‌فرض ۰۲:۰۰)

### Updater (internal/updater/updater.go)

- دانلود خودکار رنج‌های IP ایران از URL
- اعتبارسنجی: حداقل ۱۰ CIDR معتبر
- نوشتن با temp → fsync → rename
- `classifier.Reload()` برای بارگذاری داغ
- محدودیت: `io.LimitReader(resp.Body, maxReadBytes)` برای جلوگیری از Disk Exhaustion

### Metrics (internal/metrics/metrics.go)

- تمام شمارنده‌ها `atomic.Int64`
- `LearnedTodayValue()`: LearnedTotal - LearnedTotalAtMidnight
- `resetDailyStats()`: در نیمه‌شب LearnedTotalAtMidnight را ذخیره می‌کند
- `RestoreFromFile()`: بازیابی از state file در startup
- `Stop()`: با `sync.Once` (رفع race condition در نسخه قبلی)
- دو اندپوینت: `/metrics` (JSON — سازگار با sentrydps.sh) و `/metrics/prom` (فرمت متن Prometheus 0.0.4 برای اسکرپ توسط Prometheus/Grafana)

### State (internal/state/state.go)

- ذخیره JSON با temp → fsync → rename
- `Update(path, fn)`: load-modify-save با mutex سراسری
- فیلدها: LastUpdateUnix, LastUpdateSuccess, LastCleanupUnix, LearnedTodayDate, LearnedTodayCount, LearnedTotalCount, LearnedTotalAtMidnight

### Rate Limiter (internal/ratelimit/ratelimit.go)

- ۱۶ shard برای دسترسی همزمان
- sliding window ۱ ثانیه‌ای به ازای هر کلاینت IP
- پاکسازی هر ۱ دقیقه

### Global Limiter (internal/ratelimit/global.go)

- token bucket سراسری واحد (بدون shard، یک mutex)
- ظرفیت = سقف QPS (اجازه burst ۱ ثانیه‌ای)
- `global_qps_limit = 0` → غیرفعال (Allow همیشه true)
- در handler پیش از resolve اعمال می‌شود؛ شامل همه کوئری‌ها حتی cache hit

### Loop Detection (main.go, handler)

- اگر `loop_detection: true` و IP مبدأ کوئری برابر یکی از بالادستی‌ها باشد
  (iran_dns / global_dns / global_dns_fallback) → پاسخ REFUSED
- فقط IPهای بالادستیِ معتبر (قابل ParseIP) رصد می‌شوند
- جلوگیری از حلقه فوروارد: کلاینت → SentryDNS → بالادستی → SentryDNS → …
- متریک: `loop_detections`

---

## مدل همزمانی (Concurrency Model)

### گوروتین‌ها

| گوروتین               | مالک     | طول عمر | سیگنال توقف    |
| --------------------- | -------- | ------- | -------------- |
| UDP server            | main     | فرآیند  | Shutdown()     |
| TCP server            | main     | فرآیند  | Shutdown()     |
| Metrics HTTP          | main     | فرآیند  | Shutdown(ctx)  |
| Cache cleanup         | cache    | فرآیند  | cache.Stop()   |
| Store cleanup         | store    | فرآیند  | store.Stop()   |
| Updater               | updater  | فرآیند  | updater.Stop() |
| Metrics daily reset   | metrics  | فرآیند  | metrics.Stop() |
| Per-query upstream    | resolver | درخواست | پایان طبیعی    |
| Store persist flusher | store    | فرآیند  | store.Stop()   |

### ترتیب Shutdown

```txt
SIGINT/SIGTERM
  → UDP server shutdown
  → TCP server shutdown
  → Metrics HTTP shutdown
  → metrics.Stop()
  → resolver.Stop() (تا ۳۰s منتظر درخواست‌های فعال می‌ماند)
  → store.Stop()
  → updater.Stop() (اگر فعال باشد)
  → close log file
```

### Mutexها

| Mutex                     | هدف                          |
| ------------------------- | ---------------------------- |
| `classifier.mu` (RWMutex) | محافظت از بارگذاری مجدد CIDR |
| `store.mu` (RWMutex)      | محافظت از map دامنه‌ها       |
| `store.persistMu` (Mutex) | محافظت از persist buffer     |
| `cache.mu` (RWMutex)      | محافظت از کش                 |
| `state.updateMu` (Mutex)  | محافظت از state file         |
| `ratelimit`               | lock-free per shard          |
| `metrics`                 | lock-free atomics فقط        |

### کانال‌ها

- کانال‌های upstream در `resolveWithLearning`: **بافر ۱** (`make(chan *dns.Msg, 1)`)
- `stopCh` در Resolver: `make(chan struct{})`
- `stop` در Metrics: `make(chan struct{})`

**مهم:** کانال‌های upstream همیشه بافر دارند (۱). این باعث می‌شود goroutine فرستنده حتی اگر کسی نخواند، مسدود نشود (بدون نشتی).

---

## حالت‌های شکست (Failure Modes)

| شکست                                              | رفتار                                        |
| ------------------------------------------------- | -------------------------------------------- |
| IranDNS قطع است                                   | فال‌بک به GlobalDNS                          |
| GlobalDNS قطع است | دامنه‌های ایرانی به کار خود ادامه می‌دهند |
| هر دو قطع                                         | SERVFAIL                                     |
| ایران IP غیرایرانی برگرداند + GlobalDNS پاسخ ندهد | SERVFAIL (امنیتی)                            |
| iran-ranges.txt نیست                              | شروع نمی‌شود                                 |
| learned.conf نیست                                 | با حافظه خالی شروع می‌کند                    |
| state file خراب                                   | reset state                                  |
| دیسک پر                                           | ادامه کار با حافظه (ذخیره نمی‌شود)           |
| updater failed                                    | رنج‌های قبلی حفظ می‌شوند                     |
| upstream flapping                                 | singleflight + jitter جذب می‌کند             |
| TCP fallback context expired                      | context.WithTimeout جدید برای TCP            |
| Circuit breaker (Iran) open                              | IranDNS queries skipped → fallback GlobalDNS |
| Circuit breaker (Global) open                            | GlobalDNS queries skipped → fallback Iran-only, SERVFAIL اگر ایران هم جواب ندهد |
| Cleanup IranDNS unhealthy                         | پاکسازی انجام نمی‌شود                        |

### شرط امنیتی بحرانی

اگر IranDNS IP غیرایرانی برگرداند (مشکوک به تحریف) و GlobalDNS تایم‌اوت کند:
**نباید** ایران DNS را برگرداند. باید SERVFAIL برگرداند. برگرداندن IP تحریف‌شده می‌تواند کاربران را به زیرساخت‌های سانسور هدایت کند.

---

## حساس‌ترین منطق‌ها و وارون‌ناپذیری‌ها — قبل از تغییر حتماً بخوانید

### 1. هرگز IPهای تحریف‌شده را برنگردان

اگر IranDNS پاسخ با IP غیرایرانی/مشکوک برگرداند و GlobalDNS در دسترس نباشد، باید SERVFAIL برگردانده شود، نه IranDNS. پاسخ‌های تحریف‌شده مسیریابی HTTPS/CDN را بی‌صدا می‌شکنند.

### 2. هرگز تفکیک را به طور نامحدود مسدود نکن

تمام انتظارها محدود هستند. انتظار upstream از `shortWait = timeout/4` استفاده می‌کند. درخواست‌های کلاینت باید همیشه خاتمه یابند. تلاش مجدد محدود به یک بار با jitter است.

### 3. ذخیره‌سازی فایل باید crash-safe باشد

تمام نوشتن‌های فایل باید از: فایل موقت → fsync → تغییر نام استفاده کنند. هرگز مستقیم ننویسید، درجا کوتاه نکنید، یا ناقص بازنویسی نکنید. `os.Rename()` روی یک فایل‌سیستم برای جایگزینی اتمیک استفاده می‌شود.

### 4. گوروتین‌ها هرگز نباید نشت کنند

گوروتین‌های upstream مربوط به هر درخواست باید همیشه خاتمه یابند، هرگز برای همیشه مسدود نشوند، و فقط در کانال‌های بافر (`make(chan result, 1)`) بنویسند — هرگز بدون بافر.

### 5. پاسخ‌های کش باید ID اصلی کوئری را حفظ کنند

قبل از برگرداندن پاسخ‌های کش/اشتراکی: `resp = resp.Copy(); resp.Id = origID`. در غیر این صورت کلاینت‌ها دچار خطای DNS می‌شوند.

### 6. GlobalDNS goroutine cancellation

وقتی IranDNS با IP معتبر اول جواب می‌دهد، GlobalDNS goroutine **باید** بلافاصله کنسل شود. بدون این کنسل کردن، تا `globalTimeout` (۱۵۰۰ms) بی‌دلیل منتظر می‌ماند. پیاده‌سازی: `globalCtx, globalCancel := context.WithCancel(ctx)` و `defer globalCancel()`.

### 7. shortWait = globalTimeout/4

- اگر خیلی کم باشد: false SERVFAIL
- اگر خیلی زیاد باشد: latency اضافی برای دامنه‌های خارجی
- در production: `global_dns_timeout: 5` → `shortWait = 1250ms`
- حداقل: ۱۰۰ms

### 8. Non-address queries (PTR, MX, TXT) از IP classification عبور می‌کنند

PTR, MX, TXT, CNAME, SOA و کوئری‌های غیر A/AAAA **IP برنمی‌گردانند**، بنابراین وقتی IranDNS اول جواب می‌دهد، `resolveWithLearning` بلافاصله جواب را برمی‌گرداند بدون منتظر GlobalDNS. این کار از SERVFAILهای بی‌مورد جلوگیری می‌کند.

### 9. Retry jitter اجباری است

تأخیر: `timeout/15 + random(timeout/30)`. هدف: جلوگیری از retry storm و thundering herd.

### 10. ترتیب تشخیص Hijack مهم است

اول تطبیق دقیق IP (exact match)، بعد تطبیق محدوده CIDR. IPهای تحریف‌شده شناخته‌شده: 10.10.34.34-36. محدوده‌های تحریف‌شده شناخته‌شده: 50.7.0.0/16.

### 11. ValidateDomain فقط TypeA

برای کاهش بار روی IranDNS، `ValidateDomain` فقط یک کوئری TypeA می‌زند (نه AAAA). AAAAها در مسیر عادی کار می‌کنند.

### 12. Singleflight key شامل query type است

فرمت: `domain:qtype` (مثلاً `digikala.com.:A`). هرگز A و AAAA را ادغام نکنید.

### 13. Subdomain inheritance عمدی است

اگر `digikala.com` یاد گرفته شود، `cdn.digikala.com` نیز از IranDNS استفاده می‌کند.

### 14. GlobalDNS circuit breaker فقط در resolveWithLearning بررسی می‌شود

در مسیرهای TLD، PreferIran و Store، GlobalDNS صرفاً به عنوان fallback استفاده می‌شود — در این مسیرها GlobalCB بررسی نمی‌شود. فقط در `resolveWithLearning` (مسیر Learn) قبل از راه‌اندازی گوروتین GlobalDNS، مدارشکن بررسی می‌شود. این کار تضمین می‌کند دامنه‌های ایرانی حتی اگر GlobalDNS مشکل داشته باشد، به مسیر خود ادامه دهند.

---

## تنظیمات کامل (Config Reference)

### config.yaml

```yaml
# === الزامی ===
iran_dns: "172.16.0.25" # آدرس سرور DNS ایران
global_dns: "172.16.0.24" # آدرس سرور DNS جهانی
listen: ":53" # آدرس گوش دادن (پیش‌فرض :53)

# === Timeoutها ===
iran_dns_timeout: 3 # تایم‌اوت IranDNS (ثانیه)
global_dns_timeout: 5 # تایم‌اوت GlobalDNS (ثانیه، float64)

# === کش ===
min_ttl: 300 # حداقل TTL کش (ثانیه)
max_ttl: 3600 # حداکثر TTL کش (ثانیه)
cache_max_entries: 100000 # حداکثر تعداد entries کش (0 = بی‌نهایت)

# === یادگیری ===
learned: "data/learned.conf" # فایل ذخیره دامنه‌های یادگرفته شده

# === Classifier ===
iran_ranges: "data/iran-ranges.txt" # فایل رنج‌های IP ایران
iran_ranges_url: "http://..." # URL به‌روزرسانی خودکار
iran_ranges_update_interval: 24h # بازه به‌روزرسانی

# === Circuit Breaker ===
iran_cb_threshold: 10 # تعداد خطاهای متوالی برای باز کردن مدار ایران
iran_cb_cooldown: 30s # مدت زمان قبل از half-open probe

global_cb_threshold: 10 # تعداد خطاهای متوالی برای باز کردن مدار Global
global_cb_cooldown: 30s # مدت زمان قبل از half-open probe Global

# === فال‌بک ===
global_dns_fallback: "" # سرور فال‌بک Global (خالی = غیرفعال)

# === لیست‌ها ===
iran_tlds: ["ir", "ایران"]
hijack_ips: ["10.10.34.34", "10.10.34.35", "10.10.34.36"]
hijack_ranges: ["50.7.0.0/16"]
prefer_iran_domains: ["google.com", "easy4ipcloud.com"]

# === Cleanup ===
cleanup_schedule: "02:00" # زمان پاکسازی روزانه (HH:MM)
cleanup_initial_delay: 1h # تأخیر اولیه قبل از اولین پاکسازی
cleanup_qps: 100 # تعداد کارگرهای موازی پاکسازی

# === Rate Limit ===
rate_limit_per_client: 0 # 0 = غیرفعال

# === Logging ===
log_level: "info" # debug, info, warn, error
log_format: "json" # json, text
log_file: "/var/log/sentrydns/sentrydns.log"

# === Metrics ===
metrics_addr: ":9153"

# === State ===
state_file: "data/.sentrydns-state"
```

---

## Metricها و مانیتورینگ

### HTTP Endpoint

```
GET http://localhost:9153/metrics → JSON
GET http://localhost:9153/health  → {"status":"ok","uptime":"..."}
```

### Metricهای کلیدی

| Metric                             | معنی                         | راهنمای عیب‌یابی                   |
| ---------------------------------- | ---------------------------- | ---------------------------------- |
| `queries_total`                    | کل کوئری‌ها                  | نرمال: 5k-10k QPS                  |
| `queries_cached`                   | کش hits                      | نرمال >60% از total                |
| `queries_servfail`                 | خطاهای SERVFAIL              | عدد بالا = مشکل در upstreamها      |
| `queries_iran`                     | مسیریابی به ایران            | باید با `path_*` مطابقت داشته باشد |
| `queries_global`                   | مسیریابی به global           | عدد بالا = دامنه‌های خارجی زیاد    |
| `queries_retried`                  | تلاش مجدد                    | افزایش = بی‌ثباتی upstream         |
| `cache_hit_ratio`                  | درصد کش hits                 | باید >60% باشد                     |
| `iran_timeouts`                    | تایم‌اوت ایرانDNS            | افزایش = مشکل ایرانDNS             |
| `global_timeouts`                  | تایم‌اوت GlobalDNS           | نیاز به بررسی shortWait            |
| `iran_cb_open`                     | ۱ = مدار باز است             | ایرانDNS مشکل دارد                 |
| `iran_cb_skipped`                  | کوئری‌های رد شده             | همبسته با iran_cb_open             |
| `iran_cb_trips`                    | تعداد باز شدن مدار           | افزایش = مشکل پایدار ایرانDNS      |
| `global_cb_open`                   | ۱ = مدار Global باز است      | GlobalDNS مشکل دارد                 |
| `global_cb_skipped`                | GlobalDNS کوئری‌های رد شده   | همبسته با global_cb_open            |
| `global_cb_trips`                  | تعداد باز شدن مدار Global    | افزایش = مشکل پایدار GlobalDNS      |
| `short_wait_expired`               | shortWait قبل از GlobalDNS   | GlobalDNS کُندتر از shortWait       |
| `tcp_fallback_count`               | فال‌بک TCP                   | افزایش = EDNS0 یا MTU مشکل دارد    |
| `global_fallback_count`            | فال‌بک Global                | افزایش = GlobalDNS مشکل دارد       |
| `path_tld/prefer_iran/store/learn` | توزیع مسیرها                 | نشان‌دهنده ترکیب ترافیک            |
| `in_flight_queries`                | کوئری‌های در حال پردازش      | باید با QPS متناسب باشد            |
| `learned_total`                    | کل دامنه‌های یادگرفته شده    | ~180k در production                |
| `learned_today`                    | دامنه‌های یادگرفته شده امروز | نشان‌دهنده سرعت یادگیری           |
| `store_size`                       | تعداد دامنه‌های ذخیره شده    | هم‌اندازه learned_total            |

### Metricهای محاسبه شده

- `iran_avg_latency_ms` = IranLatencyTotal / IranQueryCount
- `global_avg_latency_ms` = GlobalLatencyTotal / GlobalQueryCount
- `global_fallback_avg_latency_ms` = GlobalFallbackLatencyTotal / GlobalFallbackCount
- `cache_hit_ratio` = queries_cached / queries_total × 100

---

## نحوه استقرار

### Build

```bash
GOOS=linux GOARCH=amd64 go build -o sentrydns cmd/sentrydns/main.go
```

### نصب روی سرور

```bash
make deploy SERVER=user@172.16.0.41
```

یا دستی:

```bash
make build
sudo make install
```

### Deploy از طریق SSH

اسکریپت `scripts/deploy.sh`:

1. build برای linux/amd64
2. rsync فایل‌ها به سرور
3. نصب با `scripts/install.sh`
4. سرویس‌ها را restart می‌کند

### سرویس‌های systemd

```bash
sudo systemctl start sentrydns
sudo systemctl stop sentrydns
sudo systemctl status sentrydns
sudo journalctl -u sentrydns -n 100 --no-pager
```

### Logrotate

فایل `scripts/logrotate.conf` چرخش خودکار لاگ‌ها را انجام می‌دهد.

---

## عیب‌یابی

### دسترسی به سرور production

```bash
ssh sentrydns-prod
```

### لاگ‌ها

```bash
journalctl -u sentrydns -n 500 --no-pager    # آخرین ۵۰۰ خط
journalctl -u sentrydns -f                     # دنبال کردن لاگ
tail -f /var/log/sentrydns/sentrydns.log       # لاگ فایل
```

### Metricها

```bash
curl -s http://172.16.0.41:9153/metrics | jq
curl -s http://172.16.0.41:9153/health
```

### تست DNS

```bash
dig @127.0.0.1 google.com A
dig @127.0.0.1 google.com AAAA
dig @127.0.0.1 youtube.com A
```

--- English Version ---

</div>

---

## Table of Contents

- [Architecture Overview](#architecture-overview)
- [Step-by-Step Request Flow](#step-by-step-request-flow)
- [Component Details](#component-details)
- [Concurrency Model](#concurrency-model)
- [Failure Modes](#failure-modes)
- [Critical Invariants — Read Before Changing](#critical-invariants--read-before-changing)
- [Full Configuration Reference](#full-configuration-reference)
- [Metrics & Monitoring](#metrics--monitoring)
- [Deployment](#deployment)
- [Troubleshooting](#troubleshooting)

---

## Architecture Overview

SentryDNS is an adaptive DNS proxy that automatically learns whether to route each domain to an Iranian upstream or a global upstream, with zero manual domain list management.

```txt
 Client (UDP/TCP :53)
      │
      ▼
┌──────────────┐
│   Handler    │  ← rate limit, panic recovery, timeout context
└──────┬───────┘
       │
┌──────▼───────┐
│  Resolver    │  ← 4 decision paths: TLD → preferIran → store → learn
│  + Cache     │
│  + Learning  │
└──────┬───────┘
       │
┌──────▼───────┐      ┌──────────────────┐      ┌──────────────────┐
│   query()    │─────►│  IranCB          │─────►│  GlobalCB        │
│  (upstream)  │      │  circuitBreaker  │      │  circuitBreaker  │
└──────┬───────┘      └──────────────────┘      └──────────────────┘
       │
       ├──► IranDNS  (172.16.0.25)
       └──► GlobalDNS (172.16.0.24)
               └── GlobalDNS Fallback (optional)
```

### Package Responsibilities

| Package                 | Responsibility                                     |
| ----------------------- | -------------------------------------------------- |
| `cmd/sentrydns/main.go` | Bootstrap, wiring, graceful shutdown               |
| `internal/config`       | YAML load, validation, defaults                    |
| `internal/resolver`     | Core: routing decisions, learning, circuit breaker |
| `internal/cache`        | TTL-aware DNS response cache, min 300s, max 3600s  |
| `internal/classifier`   | Iran IP CIDR loading, IsIran() lookup              |
| `internal/store`        | Learned domain persistence to file                 |
| `internal/updater`      | Periodic Iran IP range download                    |
| `internal/metrics`      | Atomic counters, daily reset, HTTP endpoint        |
| `internal/state`        | Atomic JSON persistence for metadata               |
| `internal/ratelimit`    | Per-client sliding-window rate limiter             |
| `internal/logger`       | JSON/text structured logger                        |

---

## Step-by-Step Request Flow

### 1. Entry (Handler at main.go:121)

```go
dns.HandleFunc(".", func(w dns.ResponseWriter, req *dns.Msg) {
    // 1. panic recovery
    // 2. empty question check → SERVFAIL
    // 3. rate limit → SERVFAIL
    // 4. context.WithTimeout 10s
    // 5. r.Resolve(ctx, req)
    // 6. nil → SERVFAIL
    // 7. w.WriteMsg(resp)
})
```

### 2. Decision Tree (Resolver.resolve, resolver.go:462)

```txt
resolve(ctx, req, domain)
  │
  ├─ isIranTLD(domain)?
  │     ├─ queryIranDNS() → success + non-hijacked → PathTLD++, return
  │     └─ fail/hijack → fallback GlobalDNS
  │
  ├─ isPreferIran(domain)?
  │     ├─ queryIranDNS() → success + non-hijacked + has IPs → PathPreferIran++, return
  │     └─ fail → fallback GlobalDNS
  │
  ├─ store.IsIran(domain)? (previously learned)
  │     ├─ queryIranDNS()
  │     ├─ NXDOMAIN? → Remove + re-learn (resolveWithLearning)
  │     ├─ nil/error → fallback GlobalDNS
  │     ├─ hijacked → fallback GlobalDNS
  │     └─ success → PathStore++, return
  │
  └─ unknown domain → resolveWithLearning() → PathLearn++
```

### 3. Parallel Learning (resolveWithLearning, resolver.go:246)

This is the most complex function in the system:

```go
resolveWithLearning(ctx, req, domain)
  │
  ├─ Determine if GlobalDNS is needed (A/AAAA or circuit breaker is open)
  │
  ├─ Check GlobalCB:
  │   ├─ If circuit breaker is open → skip GlobalDNS (record GlobalCBSkipped)
  │   └─ If closed → proceed
  │
  ├─ Launch parallel goroutines:
  │   ├─ IranDNS → r.query(iranCtx, reqCopy, iranDNS)
  │   └─ GlobalDNS → r.query(globalCtx, reqCopy, globalDNS) (if not skipped)
  │
  ├─ shortWait = globalTimeout/4 (minimum 100ms)
  │
  ├─ First select: wait for fastest response
  │   ├─ Iran arrives first → evaluate IPs
  │   │   ├─ Iranian IP + valid → learn (store.Add) + cancel GlobalDNS → return Iran
  │   │   ├─ Non-Iranian IP → wait another shortWait for GlobalDNS
  │   │   │   ├─ Global arrives → return Global
  │   │   │   └─ Timeout → WARN log + SERVFAIL
  │   │   └─ non-A/AAAA → return Iran immediately (no IP validation needed)
  │   │
  │   ├─ Global arrives first → cancel Iran
  │   │   ├─ Wait shortWait for Iran (learning only, keep Global)
  │   │   └─ return Global
  │   │
  │   └─ context done → SERVFAIL
  │
  ├─ If GlobalCB was open and Iran was nil → synchronous fallback to GlobalDNS
  ├─ If both were nil → synchronous fallback to GlobalDNS
  │
  └─ Nothing worked → WARN "no suitable upstream" + SERVFAIL
```

### 4. Circuit Breaker (resolver.go:47-104)

Two independent circuit breakers:

**IranCB** — Three states: `cbClosed` (0) → `cbOpen` (1) → `cbHalfOpen` (2)
- In Open state, ALL IranDNS queries are **skipped** (counted as `IranCBSkipped`)
- After `cooldown` (default 30s), one Half-Open probe is allowed
- Half-Open success → Closed; failure → back to Open
- `recordFailure()`: if Half-Open or threshold reached → Open
- `recordSuccess()`: if Half-Open → Closed; if Closed → decrement failures

**GlobalCB** — Same structure, fully isolated:
- In Open state, the GlobalDNS goroutine in `resolveWithLearning` is **not launched** (skipped)
- Skips are recorded in `GlobalCBSkipped`
- `query()` records GlobalDNS success/failure on GlobalCB
- Default threshold: 10 failures, cooldown: 30s
- Purpose: prevent querying GlobalDNS during degradation (reduces upstream load)

### 5. query() function (resolver.go:536-643)

```go
query(ctx, req, upstream)
  │
  ├─ Check context.Done()
  ├─ Determine timeout (iran: 3s, global: 1.5-5s)
  ├─ Respect parent deadline (use whichever is smaller)
  ├─ dns.Client from sync.Pool
  ├─ Set EDNS0 with 1232 bytes
  ├─ randDNSID() (atomic counter)
  ├─ ExchangeContext()
  │
  ├─ Record metrics & circuit breaker:
  │   ├─ Iran: IranQueryCount++, error→IranTimeouts+iranCb.recordFailure()
  │   └─ Global: GlobalQueryCount++, error→GlobalTimeouts+globalCb.recordFailure()
  │                           , success→globalCb.recordSuccess()
  │
  ├─ Error + global fallback enabled → retry once
  ├─ Truncated → TCP fallback
  └─ return resp
```

---

## Component Details

### Resolver (internal/resolver/resolver.go)

**Struct:**

```go
type Resolver struct {
    classifier        *classifier.Classifier
    store             *store.Store
    cache             *cache.Cache
    iranDNS, globalDNS, globalDNSFallback string
    timeout, globalTimeout atomic.Int64
    log               *slog.Logger
    metrics           *metrics.Metrics
    iranTLDs          map[string]bool
    hijackIPs         map[string]bool
    hijackRanges      []netip.Prefix
    preferIran        map[string]bool
    sf                singleflight.Group
    iranCb            *circuitBreaker   // IranDNS circuit breaker
    globalCb          *circuitBreaker   // GlobalDNS circuit breaker
    iranAddr, globalAddr string
    stopped           atomic.Bool
    stopCh            chan struct{}
    active            atomic.Int64
}
```

**Four decision paths in priority order:**

1. **TLD** (.ir, .ایران) — fastest path, no learning
2. **PreferIran** — manually specified domains (google.com, easy4ipcloud.com)
3. **Store** — previously learned domains
4. **Learn** — unknown domain → parallel query + validation + learning

**Singleflight** (resolver.go:429): coalesces concurrent queries for the same `domain:qtype`. Only one goroutine actually queries; others share the result.

**Retry** (resolver.go:432-438): on nil or SERVFAIL, retries once with jitter (`timeout/15 + random(timeout/30)`).

### Cache (internal/cache/cache.go)

- Key: `domain:Qtype` (lowercased)
- Only caches successful responses with Answer records
- TTL clamped to [minTTL, maxTTL]
- `Get()`: returns a _copy_ with request's ID restored
- `evictOne()`: when at max capacity, samples up to 100 entries, evicts oldest (approximate LRU)
- Background cleanup every 5 minutes

### Classifier (internal/classifier/classifier.go)

- Loads CIDR ranges from file
- Separate IPv4/IPv6 lists, sorted by prefix length descending (most specific first)
- `IsIran(ip)`: linear O(n) scan over ~600 CIDRs
- `Reload(path)`: atomic swap of range slices (for updater)

### Store (internal/store/store.go)

- In-memory `map[string]bool` of learned domains
- `persistBuf`: 5-second buffered append to file
- `writeAll()`: full rewrite via temp → fsync → rename
- `Remove()`: 500ms debounced rewrite
- `IsIran(domain)`: checks domain + parent domains up to TLD
- `cleanup()`: `qps` parallel workers, validates each domain via IranDNS
- `StartCleanup()`: initial delay (default 1h), scheduled time (default 02:00)

### Updater (internal/updater/updater.go)

- Downloads Iran IP ranges from URL
- Validation: minimum 10 valid CIDR entries
- Write via temp → fsync → rename
- `classifier.Reload()` for hot reload
- Safety: `io.LimitReader(resp.Body, maxReadBytes)` prevents disk exhaustion

### Metrics (internal/metrics/metrics.go)

- All counters are `atomic.Int64`
- `LearnedTodayValue()` = LearnedTotal - LearnedTotalAtMidnight
- `resetDailyStats()`: saves LearnedTotalAtMidnight at midnight
- `RestoreFromFile()`: restore from state file at startup
- `Stop()`: uses `sync.Once` (fixes race condition from previous version)

### State (internal/state/state.go)

- JSON persistence with temp → fsync → rename
- `Update(path, fn)`: load-modify-save with global mutex
- Fields: LastUpdateUnix, LastUpdateSuccess, LastCleanupUnix, LearnedTodayDate, LearnedTodayCount, LearnedTotalCount, LearnedTotalAtMidnight

### Rate Limiter (internal/ratelimit/ratelimit.go)

- 16 shards for concurrent access
- 1-second sliding window per client IP
- Cleanup every 1 minute

---

## Concurrency Model

### Goroutines

| Goroutine             | Owner    | Lifetime | Stop Signal        |
| --------------------- | -------- | -------- | ------------------ |
| UDP server            | main     | process  | Shutdown()         |
| TCP server            | main     | process  | Shutdown()         |
| Metrics HTTP          | main     | process  | Shutdown(ctx)      |
| Cache cleanup         | cache    | process  | cache.Stop()       |
| Store cleanup         | store    | process  | store.Stop()       |
| Updater               | updater  | process  | updater.Stop()     |
| Metrics daily reset   | metrics  | process  | metrics.Stop()     |
| Per-query upstream    | resolver | request  | natural completion |
| Store persist flusher | store    | process  | store.Stop()       |

### Shutdown Order

```
SIGINT/SIGTERM
  → UDP server shutdown
  → TCP server shutdown
  → Metrics HTTP shutdown
  → metrics.Stop()
  → resolver.Stop() (waits up to 30s for active queries)
  → store.Stop()
  → updater.Stop() (if active)
  → close log file
```

### Mutexes

| Mutex                     | Purpose                |
| ------------------------- | ---------------------- |
| `classifier.mu` (RWMutex) | Protect CIDR reload    |
| `store.mu` (RWMutex)      | Protect domain map     |
| `store.persistMu` (Mutex) | Protect persist buffer |
| `cache.mu` (RWMutex)      | Protect cache          |
| `state.updateMu` (Mutex)  | Protect state file     |
| `ratelimit`               | Lock-free per shard    |
| `metrics`                 | Lock-free atomics only |

### Channels

- Upstream channels in `resolveWithLearning`: **buffered** (`make(chan *dns.Msg, 1)`)
- `stopCh` in Resolver: `make(chan struct{})`
- `stop` in Metrics: `make(chan struct{})`

**Important:** Upstream channels are always buffered (size 1). This prevents goroutine leaks — the sender goroutine will never block even if nobody reads from the channel.

---

## Failure Modes

| Failure                                         | Behavior                                     |
| ----------------------------------------------- | -------------------------------------------- |
| IranDNS unreachable                             | Fallback to GlobalDNS                        |
| GlobalDNS unreachable                           | Iranian domains continue working             |
| Both downstreams                                | SERVFAIL                                     |
| Iran returns non-Iranian IP + GlobalDNS timeout | SERVFAIL (security)                          |
| iran-ranges.txt missing                         | Startup failure                              |
| learned.conf missing                            | Start with empty memory                      |
| State file corrupt                              | Reset state                                  |
| Disk full                                       | Continue in-memory (no persist)              |
| Updater failed                                  | Keep previous ranges                         |
| Upstream flapping                               | Singleflight + jitter absorb burst           |
| TCP fallback context expired                    | Fresh context.WithTimeout for TCP            |
| Circuit breaker (Iran) open                            | IranDNS queries skipped → fallback GlobalDNS |
| Circuit breaker (Global) open                          | GlobalDNS queries skipped → fallback Iran-only, SERVFAIL if Iran also fails |
| Cleanup IranDNS unhealthy                       | Cleanup is skipped                           |

### Critical Security Invariant

If IranDNS returns non-Iranian IPs (suspected manipulation) and GlobalDNS times out:
**Do NOT** return IranDNS response. Return SERVFAIL. Returning manipulated IPs may redirect users into censorship infrastructure.

---

## Critical Invariants — Read Before Changing

### 1. Never return known hijack IPs

If IranDNS returns suspicious/non-Iranian IPs and GlobalDNS is unavailable, return SERVFAIL, NOT IranDNS. Hijacked responses silently break HTTPS/CDN routing.

### 2. Never block resolution indefinitely

All waits are bounded. Upstream waits use `shortWait = timeout/4`. Client requests must always terminate. Retries are limited to one with jitter.

### 3. File persistence must remain crash-safe

All file writes MUST use: temp file → fsync → rename. Never write directly, truncate in-place, or partially overwrite. `os.Rename()` on same filesystem is relied upon for atomic replacement.

### 4. Goroutines must never leak

Per-query upstream goroutines must always terminate, must never block forever, must write into buffered channels only (`make(chan result, 1)`, never unbuffered).

### 5. Cached responses must preserve original query ID

Before returning cached/shared responses: `resp = resp.Copy(); resp.Id = origID`. Failing causes client-side DNS failures.

### 6. Singleflight key MUST include query type

Format: `domain:qtype` (e.g., `digikala.com.:A` vs `digikala.com.:AAAA`). Never collapse A and AAAA.

### 7. Subdomain inheritance is intentional

If `digikala.com` is learned, then `cdn.digikala.com` and `static.cdn.digikala.com` must also route through IranDNS.

### 8. GlobalDNS goroutine cancellation

When IranDNS responds first with a validated Iranian IP, the GlobalDNS goroutine **must** be cancelled immediately via `globalCancel()`. Separate `globalCtx/globalCancel` alongside `iranCtx/iranCancel`.

### 9. shortWait = globalTimeout/4

- Too small: false SERVFAILs (especially with Starlink cold-query latency)
- Too large: unnecessary latency for foreign domains
- Production: `global_dns_timeout: 5` → `shortWait = 1250ms`
- Minimum: 100ms

### 10. Non-address queries bypass IP classification

PTR, MX, TXT, CNAME, SOA and other non-A/AAAA query types don't return IPs. When IranDNS responds first, `resolveWithLearning` returns immediately without waiting for GlobalDNS.

### 11. Retry jitter is mandatory

Delay: `timeout/15 + random(timeout/30)`. Purpose: avoid retry storms, reduce upstream amplification, prevent thundering herd.

### 12. ValidateDomain checks A only

To reduce IranDNS load, `ValidateDomain` uses only TypeA queries (not AAAA). AAAA queries work through the normal routing path.

### 13. Hijack detection order matters

Exact IP match first, then CIDR range match. Known hijack IPs: 10.10.34.34-36. Known hijack ranges: 50.7.0.0/16.

### 14. GlobalDNS circuit breaker is checked only in resolveWithLearning

In the TLD, PreferIran, and Store paths, GlobalDNS is used as a fallback only — GlobalCB is NOT checked on these paths. The GlobalCB gate is only applied in `resolveWithLearning` (the Learn path) before launching the GlobalDNS goroutine. This ensures Iranian domains continue working even if GlobalDNS is degraded.

---

## Full Configuration Reference

### config.yaml

```yaml
# === Required ===
iran_dns: "172.16.0.25" # Iran DNS server address
global_dns: "172.16.0.24" # Global DNS server address
listen: ":53" # Listen address (default :53)

# === Timeouts ===
iran_dns_timeout: 3 # IranDNS timeout (seconds)
global_dns_timeout: 5 # GlobalDNS timeout (seconds, float64)

# === Cache ===
min_ttl: 300 # Minimum cache TTL (seconds)
max_ttl: 3600 # Maximum cache TTL (seconds)
cache_max_entries: 100000 # Max cache entries (0 = unlimited)

# === Learning ===
learned: "data/learned.conf" # Learned domains file

# === Classifier ===
iran_ranges: "data/iran-ranges.txt" # Iran IP ranges file
iran_ranges_url: "http://..." # Auto-update URL
iran_ranges_update_interval: 24h # Update interval

# === Circuit Breaker ===
iran_cb_threshold: 10 # Consecutive failures to trip IranCB
iran_cb_cooldown: 30s # Duration before half-open probe

global_cb_threshold: 10 # Consecutive failures to trip GlobalCB
global_cb_cooldown: 30s # Duration before GlobalCB half-open probe

# === Fallback ===
global_dns_fallback: "" # Global fallback DNS (empty = disabled)

# === Lists ===
iran_tlds: ["ir", "ایران"]
hijack_ips: ["10.10.34.34", "10.10.34.35", "10.10.34.36"]
hijack_ranges: ["50.7.0.0/16"]
prefer_iran_domains: ["google.com", "easy4ipcloud.com"]

# === Cleanup ===
cleanup_schedule: "02:00" # Daily cleanup time (HH:MM)
cleanup_initial_delay: 1h # Initial delay before first cleanup
cleanup_qps: 100 # Parallel cleanup workers

# === Rate Limit ===
rate_limit_per_client: 0 # 0 = disabled

# === Logging ===
log_level: "info" # debug, info, warn, error
log_format: "json" # json, text
log_file: "/var/log/sentrydns/sentrydns.log"

# === Metrics ===
metrics_addr: ":9153"

# === State ===
state_file: "data/.sentrydns-state"
```

---

## Metrics & Monitoring

### HTTP Endpoint

```
GET http://localhost:9153/metrics → JSON
GET http://localhost:9153/health  → {"status":"ok","uptime":"..."}
```

### Key Metrics

| Metric                             | Meaning               | Troubleshooting Guide                |
| ---------------------------------- | --------------------- | ------------------------------------ |
| `queries_total`                    | Total queries         | Normal: 5k-10k QPS                   |
| `queries_cached`                   | Cache hits            | Normal >60% of total                 |
| `queries_servfail`                 | SERVFAIL errors       | High number = upstream problem       |
| `queries_iran`                     | Routed to Iran        | Should match `path_*` sum            |
| `queries_global`                   | Routed to global      | High = many foreign domains          |
| `queries_retried`                  | Retried queries       | Increase = upstream instability      |
| `cache_hit_ratio`                  | Cache hit %           | Should be >60%                       |
| `iran_timeouts`                    | IranDNS timeouts      | Increase = IranDNS problem           |
| `global_timeouts`                  | GlobalDNS timeouts    | May indicate shortWait issue         |
| `iran_cb_open`                     | 1 = circuit is open   | IranDNS is having issues             |
| `iran_cb_skipped`                  | Skipped queries       | Correlates with iran_cb_open         |
| `iran_cb_trips`                    | CB open count         | Increase = persistent IranDNS issues |
| `global_cb_open`                   | 1 = GlobalCB is open  | GlobalDNS is having issues            |
| `global_cb_skipped`                | GlobalDNS skips       | Correlates with global_cb_open        |
| `global_cb_trips`                  | GlobalCB open count   | Increase = persistent GlobalDNS issues|
| `short_wait_expired`               | shortWait before Global| GlobalDNS slower than shortWait      |
| `tcp_fallback_count`               | TCP fallbacks         | Increase = EDNS0/MTU issues          |
| `global_fallback_count`            | Global fallbacks      | Increase = GlobalDNS issues          |
| `path_tld/prefer_iran/store/learn` | Path distribution     | Shows traffic composition            |
| `in_flight_queries`                | Currently processing  | Should match QPS                     |
| `learned_total`                    | Total learned domains | ~180k in production                  |
| `learned_today`                    | Learned today         | Shows learning rate                  |
| `store_size`                       | Stored domain count   | Should match learned_total           |

### Computed Metrics

- `iran_avg_latency_ms` = IranLatencyTotal / IranQueryCount
- `global_avg_latency_ms` = GlobalLatencyTotal / GlobalQueryCount
- `global_fallback_avg_latency_ms` = GlobalFallbackLatencyTotal / GlobalFallbackCount
- `cache_hit_ratio` = queries_cached / queries_total × 100

---

## Deployment

### Build

```bash
GOOS=linux GOARCH=amd64 go build -o sentrydns cmd/sentrydns/main.go
```

### Install on Server

```bash
make deploy SERVER=user@172.16.0.41
```

Or manually:

```bash
make build
sudo make install
```

### Deploy via SSH

`scripts/deploy.sh`:

1. Builds for linux/amd64
2. Rsyncs files to server
3. Runs `scripts/install.sh`
4. Restarts services

### systemd Services

```bash
sudo systemctl start sentrydns
sudo systemctl stop sentrydns
sudo systemctl status sentrydns
sudo journalctl -u sentrydns -n 100 --no-pager
```

### Logrotate

`scripts/logrotate.conf` handles automatic log rotation.

### Makefile Targets

| Target                              | Description                    |
| ----------------------------------- | ------------------------------ |
| `make build`                        | Build linux/amd64 binary       |
| `make install`                      | Build + install via install.sh |
| `make deploy SERVER=user@ip`        | Deploy to remote server        |
| `make deploy-binary SERVER=user@ip` | Deploy binary only             |
| `make start/stop/status`            | Service management             |
| `make logs`                         | Tail log file                  |
| `make test`                         | Run all tests                  |
| `make test-race`                    | Run tests with race detector   |
| `make cover`                        | Test coverage report           |
| `make lint`                         | go vet                         |
| `make download-ranges`              | Download Iran IP ranges        |
| `make download-domains`             | Download Iran-hosted domains   |

---

## Troubleshooting

### SSH to Production

```bash
ssh sentrydns-prod
```

### Logs

```bash
journalctl -u sentrydns -n 500 --no-pager    # Last 500 lines
journalctl -u sentrydns -f                     # Follow logs
tail -f /var/log/sentrydns/sentrydns.log       # Log file
```

### Metrics

```bash
curl -s http://172.16.0.41:9153/metrics | jq          # JSON snapshot
curl -s http://172.16.0.41:9153/metrics/prom          # Prometheus text format
curl -s http://172.16.0.41:9153/health
```

Prometheus: `metrics_path: /metrics/prom` — همه counters با پسوند `_total`، تأخیرها به ثانیه (gauge)، `cache_hit_ratio` کسر ۰ تا ۱.

### DNS Diagnostics

```bash
dig @127.0.0.1 google.com A
dig @127.0.0.1 google.com AAAA
dig @127.0.0.1 youtube.com A
```

### Common Issues

**SERVFAIL for foreign domains:**

- Check `global_timeouts` vs total queries ratio
- Check `shortWait` (global_dns_timeout/4) — if GlobalDNS is slower than shortWait, false SERVFAILs occur
- Check IranDNS is returning non-Iranian IPs → not necessarily a problem, but GlobalDNS must respond in time

**Circuit breaker keeps tripping:**

- Check `iran_timeouts` for IranDNS health
- Check `iran_cb_threshold` — default 10, may need tuning
- Check `iran_cb_cooldown` — default 30s

**High `learned_today`:**

- New domains being discovered → normal for growing traffic
- Could indicate domains being repeatedly removed and re-learned
- Check store cleanup logs

**Low cache hit ratio:**

- Many unique domains (long tail)
- Check min/max TTL settings
- Normal for cold start

**High TCP fallback:**

- Check EDNS0 configuration
- May indicate path MTU issues
- Could be upstream DNS not supporting EDNS0
