// Package i18n holds the message catalogue.
//
// zh-TW is the only shipped locale, but every string on the page resolves
// through here from day one. Adding English later becomes a new catalogue
// file rather than a hunt through templates for hardcoded text.
package i18n

// Catalog maps message ids to display text.
type Catalog map[string]string

// T returns the message, falling back to the id so a missing string shows up
// as an obviously wrong token rather than as blank space.
func (c Catalog) T(id string) string {
	if v, ok := c[id]; ok {
		return v
	}
	return id
}

// ZhTW is Taiwan Mandarin.
var ZhTW = Catalog{
	"site.title":    "NTUST 服務狀態",
	"site.tagline":  "台科大 Moodle、選課系統與 TigerDuck 後端的即時狀態",
	"site.subtitle": "每分鐘自動檢查，同時確認網站是否可連線以及 SSO 登入是否正常。",

	"state.up":       "正常",
	"state.degraded": "部分異常",
	"state.down":     "異常",
	"state.unknown":  "檢查暫停",
	"state.retired":  "已退役",

	"banner.ok":       "所有服務運作正常",
	"banner.degraded": "部分服務異常",
	"banner.down":     "服務中斷",
	"banner.unknown":  "尚未取得檢查結果",

	"service.moodle.name":          "Moodle 數位學習平台",
	"service.moodle.desc":          "moodle2.ntust.edu.tw — 課程、作業與成績",
	"service.courseselection.name": "選課系統",
	"service.courseselection.desc": "courseselection.ntust.edu.tw — 加退選與選課清單",
	"service.mail.name":            "NTUST WebMail",
	"service.mail.desc":            "mail.ntust.edu.tw — 校園電子郵件（Mail2000）",
	"service.querycourse.name":     "課程查詢 API",
	"service.querycourse.desc":     "querycourse.ntust.edu.tw — 課程搜尋資料來源",
	"service.tigerduck_v3.name":    "TigerDuck 後端 v3",
	"service.tigerduck_v3.desc":    "api.tigerduck.app/v3 — 目前使用中的 API 版本",

	"check.site":       "網站連線",
	"check.sso_page":   "SSO 登入頁面",
	"check.sso_login":  "SSO 登入驗證",
	"check.api":        "API 回應",
	"check.login_page": "登入頁面",
	"check.smtp":       "郵件投遞（MX）",

	"reason.unreachable":        "無法連線",
	"reason.timeout":            "連線逾時",
	"reason.http_4xx":           "伺服器回應錯誤（4xx）",
	"reason.http_5xx":           "伺服器錯誤（5xx）",
	"reason.unexpected_body":    "回應內容不符預期",
	"reason.sso_form_missing":   "SSO 登入頁面異常",
	"reason.sso_login_rejected": "SSO 登入遭拒",
	"reason.sso_bridge_missing": "SSO 轉導流程中斷",
	"reason.token_invalid":      "登入憑證無效",
	"reason.username_mismatch":  "登入帳號不符",
	"reason.breaker_open":       "為避免帳號被鎖定，暫停登入檢查",
	"reason.gate_closed":        "登入頁面異常，暫不進行登入檢查",
	"reason.not_configured":     "未設定登入憑證，僅檢查網站",
	"reason.retired":            "此版本已停止服務",
	"reason.login_form_missing": "登入頁面無法載入表單",
	"reason.mx_partial":         "部分郵件伺服器無回應",

	"label.uptime":        "可用率",
	"label.last_checked":  "最後檢查",
	"label.since":         "持續時間",
	"label.history":       "近 %d 天",
	"label.history_hours": "近 %d 小時",
	"label.no_data":       "無資料",
	"label.legend":        "圖例",
	"label.updated_at":    "資料更新於",
	"label.api":           "公開 API",
	"label.api_desc":      "所有狀態資料皆可透過公開 API 取得（有速率限制）。",
	"label.theme":         "外觀",
	"label.theme_auto":    "跟隨系統",
	"label.theme_light":   "淺色",
	"label.theme_dark":    "深色",
	"label.source":        "資料每分鐘更新，SSO 登入驗證每小時執行一次。",
	"label.tap_hint":      "點選色塊查看當日詳情",
	"label.never_checked": "尚未檢查",
	"label.range":         "時間範圍",
	"label.skip":          "跳到主要內容",
	"label.switch_lang":   "English",
	"label.unavailable":   "服務暫時無法使用",
	"label.tap_hint_hour": "點選色塊查看該小時詳情",
}
