# SentryDNS

<p align="left">
  <a href="#en">🇬🇧 English</a>
</p>

<p align="center">
  <a href="https://github.com/farshidmousavii/sentrydns/releases/tag/v1.0.0"><img src="https://img.shields.io/badge/release-v1.0.0-0b8a42" alt="v1.0.0"></a>
  <a href="https://go.dev"><img src="https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go" alt="Go 1.26+"></a>
  <a href="https://github.com/farshidmousavii/sentrydns/blob/main/LICENSE"><img src="https://img.shields.io/badge/license-MIT-yellow" alt="MIT License"></a>
</p>

<div dir="rtl" lang="fa">

**پروکسی هوشمند DNS با یادگیری خودکار — بدون نیاز به مدیریت دستی لیست دامنه‌ها**

SentryDNS به صورت خودکار مسیریابی بهینه را یاد می‌گیرد، پاسخ‌های DNS مشکوک را تشخیص می‌دهد، CDN بهتری برای سرویس‌های ایرانی فراهم می‌کند و مدیریت لیست‌های استاتیک Split-DNS را حذف می‌کند.

> **📥 دانلود نسخه v1.0.0**  
> [`sentrydns-v1.0.0.tar.gz` (6.4 مگابایت)](https://github.com/farshidmousavii/sentrydns/releases/download/v1.0.0/sentrydns-v1.0.0.tar.gz)  
> شامل: باینری + فایل‌های سرویس systemd + اسکریپت نصب + تنظیمات

---

## معرفی

DNS در ایران با سه چالش اساسی روبرو است:

1. **سرورهای DNS داخلی** ممکن است پاسخ‌های تحریف‌شده، فیلترشده یا مسدود برگردانند
2. **سرورهای DNS خارجی** برای سرویس‌های ایرانی CDN بهینه را نمی‌شناسند و IP غیربهینه برمی‌گردانند
3. **لیست‌های استاتیک Split-DNS** در مقیاس غیرقابل نگهداری هستند — روزانه هزاران دامنه تغییر می‌کنند

SentryDNS این سه مشکل را با یک موتور مسیریابی تطبیقی کاهش می‌دهد که بدون دخالت دستی، یاد می‌گیرد کدام دامنه باید به کدام سرور بالادستی هدایت شود.

---

## معماری

<div dir="ltr">

```
     Clients (UDP/TCP :53)
            │
            ▼
      ┌─────────────┐
      │  SentryDNS  │
      │  Decision   │
      │  Engine     │
      └──────┬──────┘
             │
      ┌──────┴──────┐
      │  Upstreams  │
      ├─────────────┤
      │  IranDNS    │
      │  GlobalDNS  │
      └──────┬──────┘
             │
      ┌──────┴──────┐
      │  Response   │
      │  Validation │
      ├─────────────┤
      │  Hijack     │
      │  Filtering  │
      │  IP Class.  │
      └──────┬──────┘
             │
      ┌──────┴──────┐
      │  Learning   │
      │  Cache/Store│
      └─────────────┘
```

</div>

### مسیر تصمیم‌گیری

<div dir="ltr">

```
request
  │
  ├─ Cache hit? ──────────────► return cached
  │
  ├─ Iran TLD (.ir, .ایران)?
  │     ├─ IranDNS success? ──► return IranDNS
  │     └─ fallback ──────────► return GlobalDNS
  │
  ├─ Prefer-iran domain?
  │     ├─ IranDNS success
  │     │   + non-hijacked? ──► return IranDNS
  │     └─ fallback ──────────► return GlobalDNS
  │
  ├─ Learned domain (store)?
  │     ├─ IranDNS success? ──► return IranDNS
  │     ├─ NXDOMAIN? ─────────► remove + re-learn
  │     └─ error/empty ───────► return GlobalDNS
  │
  └─ Unknown domain
        ├─ Parallel: IranDNS + GlobalDNS
        ├─ Responses evaluated after validation and classification
        ├─ Validate: hijack filtering → IP classification
        ├─ IranDNS + Iranian IP? ──► learn + return
        └─ GlobalDNS responded? ───► return (learn from Iran if possible)
```

</div>

---

## مقایسه: Split-DNS سنتی vs SentryDNS

| مسئله                | Split-DNS سنتی         | SentryDNS      |
| -------------------- | ---------------------- | -------------- |
| لیست دامنه           | دستی و غیرقابل نگهداری | یادگیری خودکار |
| بهینه‌سازی CDN       | ضعیف                   | تطبیقی         |
| تشخیص تحریف DNS      | ندارد                  | دارد           |
| انتخاب سرور بالادستی | استاتیک                | پویا           |
| خودآموز              | ندارد                  | دارد           |

---

## مدل تهدید

SentryDNS در محیطی کار می‌کند که پاسخ DNS لزوماً قابل اعتماد نیست:

- **IranDNS** ممکن است IP غیرایرانی (تحریف‌شده) برگرداند → این پاسخ‌ها رد می‌شوند
- **IranDNS** ممکن است پاسخ FAIL بدهد → فال‌بک به GlobalDNS
- **GlobalDNS** ممکن است برای دامنه‌های ایرانی CDN بهینه را نشناسد → یادگیری تدریجی مسیر درست
- **پاسخ IranDNS با IP ایرانی** → دامنه بعد از اعتبارسنجی برای یادگیری در نظر گرفته می‌شود
  (دقت وابسته به کامل بودن فایل رنج‌های IP است؛ موارد خاص شامل anycast، reverse proxy،
  و گره‌های CDN منطقه‌ای ممکن است باعث خطای دسته‌بندی شوند)
- **پاسخ IranDNS با IP غیرایرانی + تایم‌اوت GlobalDNS** → SERVFAIL (احتمال دستکاری)
- **NXDOMAIN بازنویسی شده** → ایران ممکن است NXDOMAIN برگرداند؛ با GlobalDNS cross-validate می‌شود
- **Wildcard DNS مسموم** → wildcardهای مخرب توسط فیلتر hijack حذف می‌شوند
- **ECS divergence** → IranDNS و GlobalDNS ممکن است موقعیت‌های متفاوتی ببینند؛ یادگیری تدریجی مسئله را حل می‌کند
- **CDN geolocation** → هر سرور بالادستی CDN متفاوتی برگرداند؛ مسیر بهینه یاد گرفته می‌شود
- **IPهای تحریمی/مسدود** در لایه فیلترینگ پاسخ حذف می‌شوند
- **تذکر:** دسته‌بندی IP بر اساس کامل بودن و به‌روزرسانی فایل رنج‌های ایران است

---

## ویژگی‌های Production

- ✅ استقرار خودکار با systemd و logrotate
- ✅ یادگیری پایدار در فایل (`data/learned.conf`)
- ✅ کش هوشمند با حداقل/حداکثر TTL
- ✅ خاموشی تمیز (Graceful Shutdown)
- ✅ لاگ JSON ساختاریافته (stdout + فایل)
- ✅ پاکسازی همزمان با ۱۰۰ کارگر
- ✅ به‌روزرسانی خودکار رنج‌های IP ایران
- ✅ فال‌بک TCP در صورت Truncated UDP
- ✅ حذف کوئری‌های تکراری با Singleflight
- ✅ مدارشکن مجزا برای ایرانDNS و GlobalDNS با قابلیت تنظیم threshold
- ✅ مشاهده‌پذیری shortWait (شمارنده `short_wait_expired`)

---

## پیش‌نیازها

- Go 1.26+
- systemd (لینوکس)
- دو سرور DNS بالادستی (ایران و گلوبال)

---

## نصب سریع

<div dir="ltr">

**گزینه ۱ — دریافت بسته آماده (سریع‌تر):**

```bash
wget https://github.com/farshidmousavii/sentrydns/releases/download/v1.0.0/sentrydns-v1.0.0.tar.gz
tar xzf sentrydns-v1.0.0.tar.gz
sudo bash install.sh
```

**گزینه ۲ — کامپایل از سورس:**

```bash
git clone https://github.com/farshidmousavii/sentrydns.git
cd sentrydns

make download-ranges
make download-domains

sudo make install
```

### تست

</div>

مقایسه کنید: youtube.com در IranDNS مسدود است اما SentryDNS آن را تشخیص می‌دهد:

<div dir="ltr">

```
dig @172.16.0.25 youtube.com
  → 10.10.34.35  (hijacked by IranDNS)

dig @127.0.0.1   youtube.com
  → 142.250.x.x  (real IP via GlobalDNS — SentryDNS detected the hijack)
```

</div>

---

## دانلود رنج‌های IP ایران

<div dir="ltr">

```bash
make download-ranges
```

</div>

آخرین رنج‌های IP ایران از مخزن زیر دانلود می‌شود:
https://github.com/farshidmousavii/iran-ip

فایل در `data/iran-ranges.txt` ذخیره می‌شود. این فایل برای کارکرد classifier ضروری است — بدون آن برنامه شروع نمی‌شود.

---

## دانلود دامنه‌های میزبان‌شده در ایران

<div dir="ltr">

```bash
make download-domains
```

</div>

آخرین لیست دامنه‌های میزبان‌شده در ایران از مخزن زیر دانلود می‌شود:
https://github.com/bootmortis/iran-hosted-domains

فایل در `data/learned.conf` ذخیره می‌شود.

---

## فایل learned.conf

نیازی به ایجاد دستی ندارد. اگر `data/learned.conf` وجود نداشته باشد:

- برنامه با حافظه خالی شروع می‌کند
- دامنه‌ها به مرور یاد گرفته می‌شوند
- فایل به صورت خودکار با اولین ذخیره ساخته می‌شود
- برای شروع سریعتر می‌توانید از `make download-domains` استفاده کنید

---

## تنظیمات

فایل `config.yaml` را مطابق محیط خود ویرایش کنید. نمونه کامل در `config.example.yaml` موجود است.

| پارامتر                       | توضیح                                      |
| ----------------------------- | ------------------------------------------ |
| `iran_dns`                    | آدرس سرور DNS ایران                        |
| `global_dns`                  | آدرس سرور DNS گلوبال                       |
| `listen`                      | پورت گوش دادن (پیش‌فرض: ۵۳)                |
| `min_ttl`                     | حداقل TTL کش (پیش‌فرض: ۳۰۰)                |
| `max_ttl`                     | حداکثر TTL کش (پیش‌فرض: ۳۶۰۰)              |
| `iran_tlds`                   | فهرست TLDهای ایران                         |
| `hijack_ips`                  | IPهای مسدود/تحریمی                         |
| `hijack_ranges`               | رنج‌های IP مسدود/تحریمی                    |
| `prefer_iran_domains`         | دامنه‌هایی که از IranDNS IP بهتری می‌گیرند |
| `iran_ranges_url`             | آدرس به‌روزرسانی خودکار رنج‌های IP ایران   |
| `iran_ranges_update_interval` | بازه به‌روزرسانی خودکار (پیش‌فرض: ۲۴h)     |
| `iran_cb_threshold`           | تعداد خطاهای متوالی برای باز کردن مدار ایران |
| `iran_cb_cooldown`            | مدت زمان قبل از half-open probe ایران (پیش‌فرض: ۳۰s) |
| `global_cb_threshold`         | تعداد خطاهای متوالی برای باز کردن مدار Global |
| `global_cb_cooldown`          | مدت زمان قبل از half-open probe Global (پیش‌فرض: ۳۰s) |

---

## مدیریت سرویس

<div dir="ltr">

```bash
sudo make start
sudo make stop
sudo make status
sudo make logs
```

یا:

```bash
sudo systemctl start sentrydns
sudo systemctl stop sentrydns
sudo systemctl status sentrydns
sudo journalctl -u sentrydns -f
```

</div>

---

## استقرار روی سرور راه دور

<div dir="ltr">

```bash
make deploy SERVER=user@server-ip
```

</div>

---

## انتشار نسخه‌ها

<div dir="rtl">

باینری‌های آماده در [صفحه انتشار](https://github.com/farshidmousavii/sentrydns/releases) در دسترس هستند.

هر نسخه شامل:
- **sentrydns** — باینری پروکسی DNS (لینوکس amd64)
- **sentrydns.service** — فایل سرویس systemd
- **sentrydps.service** — فایل سرویس پروب تشخیصی
- **install.sh** — اسکریپت نصب خودکار (ساخت سرویس systemd + نصب باینری)
- **uninstall.sh** — اسکریپت حذف
- **sentrydps.sh** — اسکریپت پروب تشخیصی
- **config.example.yaml** — مرجع کامل تنظیمات

</div>

</div>

---

<a name="en"></a>

# SentryDNS

<p align="center">
  <a href="https://github.com/farshidmousavii/sentrydns/releases/tag/v1.0.0"><img src="https://img.shields.io/badge/release-v1.0.0-0b8a42" alt="v1.0.0"></a>
  <a href="https://go.dev"><img src="https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go" alt="Go 1.26+"></a>
  <a href="https://github.com/farshidmousavii/sentrydns/blob/main/LICENSE"><img src="https://img.shields.io/badge/license-MIT-yellow" alt="MIT License"></a>
</p>

**Adaptive DNS proxy with automatic learning — zero manual domain list management.**

SentryDNS automatically learns optimal upstream routing, detects suspicious DNS responses, improves CDN locality for Iranian services, and eliminates static split-DNS domain management.

---

## Introduction

DNS in Iran faces three fundamental challenges:

1. **Iranian DNS servers** may return filtered, hijacked, or manipulated responses
2. **Global DNS servers** do not know optimal CDN endpoints for Iranian services, returning suboptimal IPs
3. **Static split-DNS domain lists** are impossible to maintain at scale — thousands of domains change daily

SentryDNS mitigates all three with an adaptive routing engine that learns which upstream to use for each domain — with zero manual intervention.

---

## Architecture

```
     Clients (UDP/TCP :53)
            │
            ▼
      ┌─────────────┐
      │  SentryDNS  │
      │  Decision   │
      │  Engine     │
      └──────┬──────┘
             │
      ┌──────┴──────┐
      │  Upstreams  │
      ├─────────────┤
      │  IranDNS    │
      │  GlobalDNS  │
      └──────┬──────┘
             │
      ┌──────┴──────┐
      │  Response   │
      │  Validation │
      ├─────────────┤
      │  Hijack     │
      │  Filtering  │
      │  IP Class.  │
      └──────┬──────┘
             │
      ┌──────┴──────┐
      │  Learning   │
      │  Cache/Store│
      └─────────────┘
```

### Decision Flow

```
request
  │
  ├─ Cache hit? ──────────────► return cached
  │
  ├─ Iran TLD (.ir, .ایران)?
  │     ├─ IranDNS success? ──► return IranDNS
  │     └─ fallback ──────────► return GlobalDNS
  │
  ├─ Prefer-iran domain?
  │     ├─ IranDNS success
  │     │   + non-hijacked? ──► return IranDNS
  │     └─ fallback ──────────► return GlobalDNS
  │
  ├─ Learned domain (store)?
  │     ├─ IranDNS success? ──► return IranDNS
  │     ├─ NXDOMAIN? ─────────► remove + re-learn
  │     └─ error/empty ───────► return GlobalDNS
  │
  └─ Unknown domain
        ├─ Parallel: IranDNS + GlobalDNS
        ├─ Responses evaluated after validation and classification
        ├─ Validate: hijack filtering → IP classification
        ├─ IranDNS + Iranian IP? ──► learn + return
        └─ GlobalDNS responded? ───► return (learn from Iran if possible)
```

---

## Comparison: Traditional Split-DNS vs SentryDNS

| Problem              | Traditional Split-DNS  | SentryDNS          |
| -------------------- | ---------------------- | ------------------ |
| Domain lists         | Manual, unmaintainable | Automatic learning |
| CDN optimization     | Weak                   | Adaptive           |
| DNS hijack detection | No                     | Yes                |
| Upstream selection   | Static                 | Dynamic            |
| Self-learning        | No                     | Yes                |

---

## Threat Model

SentryDNS operates in an environment where DNS responses are not necessarily trustworthy:

- **IranDNS** may return non-Iranian (hijacked) IPs → these responses are discarded
- **IranDNS** may return failure → fallback to GlobalDNS
- **GlobalDNS** may not know optimal CDN endpoints for Iranian services → gradual learning corrects this
- **IranDNS response with Iranian IP** → domain is considered eligible for learning after validation
  (accuracy depends on complete/current IP range data; edge cases include anycast, reverse proxies,
  and regional CDN nodes)
- **IranDNS response with non-Iranian IP + GlobalDNS timeout** → SERVFAIL (potential manipulation)
- **NXDOMAIN rewriting** → IranDNS may falsely return NXDOMAIN for accessible domains; cross-validated with GlobalDNS
- **Wildcard DNS poisoning** → rogue wildcard entries filtered via hijack IP/ranges
- **ECS (EDNS Client Subnet) divergence** — IranDNS and GlobalDNS may see different client locations; learning path handles this organically over time
- **Inconsistent CDN geolocation** — different upstreams may resolve to different CDN nodes; preferred routing learned per-domain
- **Sanctioned/blocked IPs** are filtered at the response validation layer
- **Note:** IP classification depends on the completeness and currency of the Iran IP ranges file

---

## Production Features

- ✅ Deployment automation (systemd, logrotate)
- ✅ File-persisted learning (`data/learned.conf`)
- ✅ TTL-aware caching with min/max clamping
- ✅ Graceful shutdown
- ✅ JSON structured logging (stdout + file)
- ✅ Concurrent cleanup (100 workers)
- ✅ Auto-updater for Iran IP ranges
- ✅ TCP fallback on truncated UDP
- ✅ Singleflight deduplication
- ✅ Independent circuit breakers for IranDNS and GlobalDNS (configurable threshold/cooldown)
- ✅ shortWait observability (`short_wait_expired` counter)

---

## Prerequisites

- Go 1.26+
- systemd (Linux)
- Two upstream DNS servers (Iran & Global)

---

## Quick Install

**Option 1 — Download release tarball (faster):**

```bash
wget https://github.com/farshidmousavii/sentrydns/releases/download/v1.0.0/sentrydns-v1.0.0.tar.gz
tar xzf sentrydns-v1.0.0.tar.gz
sudo bash install.sh
```

**Option 2 — Build from source:**

```bash
git clone https://github.com/farshidmousavii/sentrydns.git
cd sentrydns

make download-ranges
make download-domains

sudo make install
```

### Test

Compare: youtube.com is blocked/hijacked in IranDNS, but SentryDNS detects it:

```
dig @172.16.0.25 youtube.com
  → 10.10.34.35  (hijacked by IranDNS)

dig @127.0.0.1   youtube.com
  → 142.250.x.x  (real IP via GlobalDNS — SentryDNS detected the hijack)
```

---

## Download Iran IP Ranges

```bash
make download-ranges
```

Downloads the latest Iran IP ranges from:
https://github.com/farshidmousavii/iran-ip

Saved to `data/iran-ranges.txt`. This file is required — the classifier will not start without it.

---

## Download Iran-Hosted Domains

```bash
make download-domains
```

Downloads the latest list of Iran-hosted domains from:
https://github.com/bootmortis/iran-hosted-domains

Saved to `data/learned.conf`.

---

## learned.conf

If `data/learned.conf` does not exist:

- The program starts with an empty domain map
- Domains are learned organically as queries arrive
- The file is created automatically on first persist
- Use `make download-domains` for a head start

---

## Configuration

Edit `config.yaml` to match your environment. A full example is at `config.example.yaml`.

| Parameter                     | Description                             |
| ----------------------------- | --------------------------------------- |
| `iran_dns`                    | Iran upstream DNS address               |
| `global_dns`                  | Global upstream DNS address             |
| `listen`                      | Listen port (default: 53)               |
| `min_ttl`                     | Minimum cache TTL (default: 300)        |
| `max_ttl`                     | Maximum cache TTL (default: 3600)       |
| `iran_tlds`                   | Iran TLD list                           |
| `hijack_ips`                  | Blocked/sanctioned IPs                  |
| `hijack_ranges`               | Blocked/sanctioned IP ranges            |
| `prefer_iran_domains`         | Domains that resolve better via IranDNS |
| `iran_ranges_url`             | Auto-update URL for Iran IP ranges      |
| `iran_ranges_update_interval` | Auto-update interval (default: 24h)     |
| `iran_cb_threshold`           | IranCB consecutive failures to trip     |
| `iran_cb_cooldown`            | IranCB duration before half-open probe  |
| `global_cb_threshold`         | GlobalCB consecutive failures to trip   |
| `global_cb_cooldown`          | GlobalCB duration before half-open probe|

---

## Service Management

```bash
sudo make start
sudo make stop
sudo make status
sudo make logs
```

Or:

```bash
sudo systemctl start sentrydns
sudo systemctl stop sentrydns
sudo systemctl status sentrydns
sudo journalctl -u sentrydns -f
```

---

## Remote Deploy

```bash
make deploy SERVER=user@server-ip
```

---

## Releases

Pre-built binaries are available on the [releases page](https://github.com/farshidmousavii/sentrydns/releases).

Each release includes:
- **sentrydns** — the DNS proxy binary (Linux amd64)
- **sentrydns.service** — systemd unit file for the DNS proxy
- **sentrydps.service** — systemd unit file for the diagnostic probe
- **install.sh** — automated install script (sets up systemd services, copies binary)
- **uninstall.sh** — uninstall script
- **sentrydps.sh** — diagnostic probe script
- **config.example.yaml** — full configuration reference

```bash
# Download and install latest release
wget https://github.com/farshidmousavii/sentrydns/releases/download/v1.0.0/sentrydns-v1.0.0.tar.gz
tar xzf sentrydns-v1.0.0.tar.gz
sudo bash install.sh
```

---

## License

MIT License — see [LICENSE](LICENSE) for details.
