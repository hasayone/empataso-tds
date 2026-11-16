package admin

import (
	"strconv"
	"strings"
	"testing"
)

// TestSPARatingCard checks the rating card: it is the shared notice card on top of the toast stack, five stars in a
// radio group with a caption per level, the Telegram link that opens in a new tab, and the closed flag on the account.
// t is the test handle. It returns nothing and fails when a piece of the card is missing.
func TestSPARatingCard(t *testing.T) {
	html := spaSource()
	must(t, html, "rate:'star'", "the rating tone has the star mark")
	must(t, html, ".ntc.rate{--nt:var(--warn)", "the rating tone colours the shared card")
	must(t, html, "body:has(.scrim,.modal,#savebar) .ratecard,body.ai-open .ratecard{display:none}", "the card steps aside for windows, the save bar and the assistant")
	must(t, html, "@media (prefers-reduced-motion:reduce){.ratecard{animation:none}", "reduced motion stops the card animation")

	ask := spaFunctionLine(html, "rateAsk")
	must(t, ask, "ntcHTML({tone:'rate'", "the card is the shared notice card")
	must(t, ask, `role="radiogroup"`, "stars form a radio group")
	must(t, ask, `role="radio" aria-checked="${n===cur}"`, "every star is a radio, the current score is checked")
	must(t, ask, `class="rate-star${n<=cur?' on':''}"`, "the current score is lit")
	must(t, ask, `aria-label="${esc(n+' — '+rateLevel(n))}"`, "every star names its level")
	must(t, ask, "rateTgLink()", "the Telegram link is shown before a score")
	must(t, ask, `data-rateact="hide"`, "the Hide button")
	must(t, ask, "close:true", "the close cross")
	must(t, spaFunctionLine(html, "rateThanks"), "tr('rate.thanks')", "thanks after a score")
	must(t, spaFunctionLine(html, "rateThanks"), "rateTgLink()", "the Telegram link stays after a score")
	must(t, spaFunctionLine(html, "rateTgLink"), `target="_blank" rel="noopener noreferrer"`, "the link opens a new tab safely")
	must(t, html, "RATE_TG='https://t.me/empat_dev'", "the developer's Telegram")

	keys := spaFunctionLine(html, "rateKeys")
	for _, k := range []string{"Escape", "Enter", "ArrowRight", "ArrowLeft", "Home", "End"} {
		must(t, keys, k, "key "+k)
	}
	must(t, spaFunctionLine(html, "rateShow"), "box.prepend(el)", "the card is the first one of the toast stack")
	must(t, spaFunctionLine(html, "rateSave"), "closed:true", "closing is stored on the account")
	must(t, spaFunctionLine(html, "rateClose"), "rateSave(0)", "closing without a score is stored too")
	must(t, spaFunctionLine(html, "rateTick"), "call('GET','/api/me/views')", "the account is read again before showing")
	must(t, spaFunctionLine(html, "rateBusy"), "KT_DOCK.el&&!KT_DOCK.el.hidden", "the card waits for the Keitaro window")
	must(t, spaFunctionLine(html, "rateCount"), "now-RATE.saved<RATE_SAVE_MS", "active time is written at most once a minute")
	must(t, spaFunctionLine(html, "app"), "rateStart(ME)", "the app starts the count")
	must(t, spaFunctionLine(html, "rateFocused"), "ratingTabActive(document.visibilityState, document.hasFocus())", "only the tab in focus counts")
	must(t, spaFunctionLine(html, "rateCount"), "localStorage.setItem(rateSharedKey(), String(ratingTotal(", "tabs share the larger active time")
	must(t, spaFunctionLine(html, "rateWrite"), "await pullPageViews();", "fresh views are read before a write")
	must(t, spaFunctionLine(html, "rateSave"), "rateWrite(change)", "closing goes through the fresh read")
	must(t, html, "addEventListener('blur', rateFocusChange)", "losing focus ends the count")
	for _, fn := range []string{"loginView", "setupView"} {
		must(t, spaFunctionLine(html, fn), "rateStop()", fn+" stops the count")
	}
	must(t, spaFunctionLine(html, "collectPageViews"), "out.rating=pageViewRating(state.rating)", "the rating is sent with the views")
	must(t, spaFunctionLine(html, "applyPageViews"), "out.rating=pageViewRating(rating)", "the rating is read from the views")
	ru := map[int]string{1: "Отлично", 2: "Охуенно", 3: "Бля я аж заплакал", 4: "Господи как охуетительно!", 5: "СУКА Я ТЕБЕ ДЕНЕГ КИНУ! ТЫ БОГ!"}
	for n, text := range ru {
		must(t, html, "'rate.l"+strconv.Itoa(n)+"':'"+text+"'", "ru caption of level "+strconv.Itoa(n))
	}
	for _, lang := range []string{"en", "ru", "uk"} {
		for _, key := range []string{"rate.l1", "rate.l2", "rate.l3", "rate.l4", "rate.l5", "rate.title", "rate.thanks", "rate.tg", "rate.your"} {
			if !trKeyDefined(html, lang, key) {
				t.Errorf("tr(%q) missing for lang %q", key, lang)
			}
		}
	}
	for n := 1; n <= 5; n++ {
		if c := strings.Count(html, "'rate.l"+strconv.Itoa(n)+"':"); c != 3 {
			t.Errorf("rate.l%d is defined %d times, want once per language", n, c)
		}
	}
}

// TestSPARatingMenu checks where the rating card is opened by hand: not from the profile menu but from the
// "Platform rating" card of the profile, shown only to the main administrator. The card shows the current score or
// "Not rated yet", its button opens the rating card whatever the active time and the closed flag, and a new score
// redraws the card at once.
// t is the test handle. It returns nothing and fails when the profile card or the reopening is missing.
func TestSPARatingMenu(t *testing.T) {
	html := spaSource()
	menu := spaFunctionLine(html, "userMenuItems")
	mustNot(t, menu, "rateOpen", "the profile menu has no rating item")
	mustNot(t, menu, "rate.", "the profile menu has no rating caption")

	page := spaFunctionLine(html, "settings")
	must(t, page, "const rateCard=ratingOwner(ME)?`<div class=\"card pad\" id=\"rate-card\">", "the profile card is for the main administrator only")
	must(t, page, "${rateProfileHTML()}", "the card shows the score and the button")
	must(t, page, "${rateCard}", "the card is on the profile page")
	must(t, page, "if(e.target.closest('[data-rateopen]')) rateOpen()", "the button opens the rating card")

	body := spaFunctionLine(html, "rateProfileHTML")
	must(t, body, "trFill('rate.your',{n})", "the current score")
	must(t, body, "tr('rate.none')", "not rated yet")
	must(t, body, "tr(n?'rate.change':'rate.do')", "the button says Rate or Change rating")
	must(t, body, "data-rateopen", "the button is marked")
	must(t, spaFunctionLine(html, "rateSave"), "rateProfilePaint();", "a new score redraws the profile card at once")

	open := spaFunctionLine(html, "rateOpen")
	must(t, open, "rateShow();", "the button shows the card")
	mustNot(t, open, "ratingDue", "the button ignores the active time and the closed flag")
	must(t, spaFunctionLine(html, "rateShow"), "RATE.cur=pageViewRating(RATING).score;", "the card marks the current score")
	must(t, spaFunctionLine(html, "rateClose"), "if(!pageViewRating(RATING).closed) rateSave(0);", "closing keeps the closed flag")

	must(t, html, "'rate.card':'Оценка платформы'", "ru title of the profile card")
	mustNot(t, html, "'rate.menu':", "the menu caption is gone")
	for _, key := range []string{"rate.card", "rate.none", "rate.do", "rate.change"} {
		for _, lang := range []string{"en", "ru", "uk"} {
			if !trKeyDefined(html, lang, key) {
				t.Errorf("tr(%q) missing for lang %q", key, lang)
			}
		}
		if c := strings.Count(html, "'"+key+"':'") - strings.Count(html, "'"+key+"':'rate."); c != 3 {
			t.Errorf("%s is defined %d times, want once per language", key, c)
		}
	}
}
