# is-ntust-up

台科大服務狀態頁 — NTUST Moodle、選課系統、校園信箱、課程查詢 API，以及 TigerDuck 後端 v3 的狀態監控。

每個 NTUST 服務都回答兩個問題，而不只是一個：

1. **網站是否可連線？**
2. **SSO 登入是否真的能用？**

第二個問題是一般監控服務答不出來的，也是最重要的：NTUST 的邊緣伺服器可以正常回應頁面，但背後的登入完全壞掉。

## 快速開始

```bash
cp -n .env.example .env   # -n: never clobber an existing .env
# 填入 NTUST_SSO_USERNAME / NTUST_SSO_PASSWORD（可留空，僅做網站檢查）

make verify-sso   # 確認憑證可用（每個服務只嘗試一次）
make up           # 啟動 app + postgres
```

頁面在 <http://localhost:8080>。

未填憑證時服務仍會啟動，SSO 檢查會顯示「未設定登入憑證，僅檢查網站」。

## 公開 API

所有資料都可透過公開 API 取得，有速率限制（預設每分鐘 60 次）。

| Endpoint | 說明 |
|---|---|
| `GET /api/v1/status` | 所有服務與檢查項目的目前狀態 |
| `GET /api/v1/services` | 服務清單與名稱 |
| `GET /api/v1/services/{key}/history?days=90` | 每日可用率 |
| `GET /api/v1/services/{key}/history?resolution=hour&hours=24` | 每小時可用率 |
| `GET /healthz` | 監控服務本身的健康狀態（不受速率限制） |

回應帶有 `ETag` 與 `Cache-Control: public, max-age=30`，請盡量使用快取。
`RateLimit-Limit` / `RateLimit-Remaining` / `RateLimit-Reset` 會出現在每個回應；
超過限制時回傳 `429` 與 `Retry-After`。

`reason_code` 是一組固定的代碼（`unreachable`、`sso_login_rejected`、
`breaker_open` 等），不會包含上游的原始錯誤內容。

## 校園信箱

`mail.ntust.edu.tw` 是架在校內的 Mail2000（Openfind），**不走 ssoam2 SSO**，有自己的 `USERID` / `PASSWD` 登入，而且登入表單有 CAPTCHA 與 client-side challenge-response。因此這裡沒有像 Moodle、選課系統那樣的「登入驗證」檢查 —— 不會、也不應該自動繞過 CAPTCHA。

不需要憑證就能驗證的是：

- **網站連線** — HTTPS 可連。
- **登入頁面** — `/cgi-bin/login` 確實產生了含 `USERID` / `PASSWD` / `CLIENT_TOKEN` 的表單。首頁是 Apache 直接送的靜態檔，後端 CGI 掛掉時它仍然回 200，所以這項檢查才是「信箱是不是真的活著」的分界。
- **郵件投遞（MX）** — 連到 `mg1` / `mg2` 的 25 埠並確認 `220` 問候。**預設關閉**：很多主機商會擋 outbound 25，開著會用與 NTUST 無關的理由把服務標成異常。只有一台 MX 有回應時記為「部分異常」，因為信還是送得出去，但備援已經少了一台。

## 語言

依瀏覽器的 `Accept-Language` 自動選擇：任何中文變體（正體、簡體、任何地區）顯示正體中文，其餘一律回退英文。右上角的切換鈕可以覆寫（`?lang=zh-TW` / `?lang=en`）。

頁面與 API 的翻譯內容都帶 `Vary: Accept-Language`，否則共用快取會把某個訪客的語言送給下一個人。

## 兩種時間刻度

每個服務都有兩條狀態條，右上角的切換鈕決定顯示哪一條，選擇會記在瀏覽器裡：

- **近 24 小時** — 一格一小時，預設。回答「現在是不是壞的、今天什麼時候開始壞的」。
- **近 90 天** — 一格一天。回答「這陣子是不是常出問題」。

兩條都在伺服器端一次算好、一起送出，切換不需要再打一次網路。一天的格子沒辦法回答「今天早上有沒有掛」——二十四小時裡壞掉一小時，那一天看起來還是綠的。

小時資料存在 `check_hourly`，與 `check_daily` 分開，保留 `HOURLY_RETENTION_DAYS` 天。直接從原始樣本算會讓狀態條受限於 `RAW_RESULT_RETENTION_DAYS`，而且每次開頁面都要掃一次原始資料表。

## 啟動行為

服務啟動時會先把所有檢查跑過一輪，不等待間隔，頁面因此在幾秒內就有完整資料，而不是等一個完整週期。之後才各自進入自己的節奏（起始時間仍會錯開，避免每輪都同時打 NTUST）。

這一輪分兩階段：

1. **未受保護的檢查**並行執行 —— 網站、登入頁面、API。
2. **帶憑證的 SSO 登入檢查**接在後面，因為它的前置條件（登入頁面是否正常）讀的正是第一階段剛寫入的狀態。跟第一階段一起發，讀到的會是上一輪留下的舊資料。

啟動時若該登入檢查已經有比自身間隔更新的結果，就會跳過並記錄一行 log。否則一個不斷重啟的容器會在每次啟動時都對 NTUST 送出一次真實登入 —— 每日上限雖然擋得住，但為了一個已經知道答案的問題再花一次登入，只有風險沒有收穫。

## 帳號鎖定保護

NTUST Moodle 約十次失敗登入就會鎖定帳號，而且 SSO 會依 IP 限流。監控服務若每次都嘗試登入，遲早會把帳號鎖住 —— 而且會發生在 NTUST 出問題、最需要監控的時候。

因此有三層保護，全部由 scheduler 執行：

1. **便宜的檢查是昂貴檢查的前提。** 未帶憑證的結構檢查每 5 分鐘確認登入頁面是否正常；只有在它正常時，才會進行真正的登入檢查。SSO 已經壞掉時，不會浪費登入次數去確認一件已知的事。
2. **斷路器。** 連續失敗達門檻後暫停登入檢查，並以指數退避（1h → 2h → 4h⋯）恢復。憑證遭拒會**立即**觸發 —— 錯的密碼重試不會成功，重試才是導致封鎖的原因。另有每 24 小時的嘗試上限。
3. **斷路器開啟時顯示「檢查暫停」，不是「異常」。** 狀態頁不應該報告一個它沒有實際驗證過的故障。

斷路器狀態存在資料庫，容器重啟不會重置退避時間。

## 開發

```bash
make test    # 全部測試（不需要資料庫、不會連到 NTUST）
make lint    # go vet + gofmt 檢查
make build
```

測試以 `httptest` 假造整條 SSO 轉導鏈，斷路器則以注入的時鐘測試，因此登入鎖定的保護邏輯可以在毫秒內驗證，不需要真的等上數小時，也不會碰到 NTUST。

## 架構

```
config  →  probe  →  scheduler  →  store  →  web
```

- `internal/probe` — 各項檢查。不知道儲存與排程的存在。
- `internal/scheduler` — 節奏、抖動、斷路器。唯一有權「決定不執行檢查」的元件。
- `internal/store` — Postgres：原始結果、每日彙總、目前狀態、斷路器狀態。
- `internal/web` — zh-TW 頁面、公開 API、速率限制。

服務與檢查項目的清單寫在程式碼裡（`internal/config/registry.go`），只有網址、路徑與頻率來自環境變數 —— `.env` 打錯字不應該讓公開頁面悄悄少一列。

SSO 流程移植自 TigerDuck Android 客戶端。**絕對不要**改用 `POST /login/token.php`：NTUST Moodle 只支援 OIDC，那個端點會把每次嘗試都算成失敗登入。
