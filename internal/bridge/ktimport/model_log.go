package ktimport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ConversionRow — одна строка POST /admin_api/v1/conversions/log.
type ConversionRow struct {
	SubID            string     `json:"sub_id"`
	Status           string     `json:"status"`
	Revenue          FlexFloat  `json:"revenue"`
	Cost             FlexFloat  `json:"cost"`
	CampaignID       FlexInt    `json:"campaign_id"`
	StreamID         FlexInt    `json:"stream_id"`
	OfferID          FlexInt    `json:"offer_id"`
	LandingID        FlexInt    `json:"landing_id"`
	NetworkID        FlexInt    `json:"affiliate_network_id"`
	SourceID         FlexInt    `json:"ts_id"`
	Campaign         string     `json:"campaign"`
	Stream           string     `json:"stream"`
	CountryCode      string     `json:"country_code"`
	City             string     `json:"city"`
	Region           string     `json:"region"`
	DeviceType       string     `json:"device_type"`
	OS               string     `json:"os"`
	Browser          string     `json:"browser"`
	Operator         string     `json:"operator"`
	IP               string     `json:"ip"`
	UserAgent        string     `json:"user_agent"`
	PostbackDatetime string     `json:"postback_datetime"`
	ClickDatetime    string     `json:"click_datetime"`
	PreviousStatus   string     `json:"previous_status"`
	OriginalStatus   string     `json:"original_status"`
	TID              FlexString `json:"tid"`
	Language         string     `json:"language"`
	OSVersion        string     `json:"os_version"`
	BrowserVersion   string     `json:"browser_version"`
	DeviceModel      string     `json:"device_model"`
	Referrer         string     `json:"referrer"`
	SearchEngine     string     `json:"search_engine"`
	Keyword          string     `json:"keyword"`
	ISP              string     `json:"isp"`
	AdCampaignID     FlexString `json:"ad_campaign_id"`
	ExternalID       FlexString `json:"external_id"`
	CreativeID       FlexString `json:"creative_id"`
	Subs             [12]string
}

// UnmarshalJSON читает строку конверсии и складывает sub_id_1..sub_id_12 в Subs.
//
// Параметры: data — JSON одной строки журнала.
// Результат: поля строки заполнены. Subs[0] — это sub_id_1.
// Ошибки: тело не объект или числовое поле не число и не числовая строка.
func (r *ConversionRow) UnmarshalJSON(data []byte) error {
	var aux struct {
		SubID            string     `json:"sub_id"`
		Status           string     `json:"status"`
		Revenue          FlexFloat  `json:"revenue"`
		Cost             FlexFloat  `json:"cost"`
		CampaignID       FlexInt    `json:"campaign_id"`
		StreamID         FlexInt    `json:"stream_id"`
		OfferID          FlexInt    `json:"offer_id"`
		LandingID        FlexInt    `json:"landing_id"`
		NetworkID        FlexInt    `json:"affiliate_network_id"`
		SourceID         FlexInt    `json:"ts_id"`
		Campaign         string     `json:"campaign"`
		Stream           string     `json:"stream"`
		CountryCode      string     `json:"country_code"`
		City             string     `json:"city"`
		Region           string     `json:"region"`
		DeviceType       string     `json:"device_type"`
		OS               string     `json:"os"`
		Browser          string     `json:"browser"`
		Operator         string     `json:"operator"`
		IP               string     `json:"ip"`
		UserAgent        string     `json:"user_agent"`
		PostbackDatetime string     `json:"postback_datetime"`
		ClickDatetime    string     `json:"click_datetime"`
		PreviousStatus   string     `json:"previous_status"`
		OriginalStatus   string     `json:"original_status"`
		TID              FlexString `json:"tid"`
		Language         string     `json:"language"`
		OSVersion        string     `json:"os_version"`
		BrowserVersion   string     `json:"browser_version"`
		DeviceModel      string     `json:"device_model"`
		Referrer         string     `json:"referrer"`
		SearchEngine     string     `json:"search_engine"`
		Keyword          string     `json:"keyword"`
		ISP              string     `json:"isp"`
		AdCampaignID     FlexString `json:"ad_campaign_id"`
		ExternalID       FlexString `json:"external_id"`
		CreativeID       FlexString `json:"creative_id"`
		logSubs
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*r = ConversionRow{
		SubID: aux.SubID, Status: aux.Status, Revenue: aux.Revenue, Cost: aux.Cost,
		CampaignID: aux.CampaignID, StreamID: aux.StreamID, OfferID: aux.OfferID, LandingID: aux.LandingID,
		NetworkID: aux.NetworkID, SourceID: aux.SourceID, Campaign: aux.Campaign, Stream: aux.Stream,
		CountryCode: aux.CountryCode, City: aux.City, Region: aux.Region, DeviceType: aux.DeviceType,
		OS: aux.OS, Browser: aux.Browser, Operator: aux.Operator, IP: aux.IP, UserAgent: aux.UserAgent,
		PostbackDatetime: aux.PostbackDatetime, ClickDatetime: aux.ClickDatetime,
		PreviousStatus: aux.PreviousStatus, OriginalStatus: aux.OriginalStatus, TID: aux.TID,
		Language: aux.Language, OSVersion: aux.OSVersion, BrowserVersion: aux.BrowserVersion,
		DeviceModel: aux.DeviceModel, Referrer: aux.Referrer, SearchEngine: aux.SearchEngine,
		Keyword: aux.Keyword, ISP: aux.ISP, AdCampaignID: aux.AdCampaignID,
		ExternalID: aux.ExternalID, CreativeID: aux.CreativeID,
		Subs: aux.pack(),
	}
	return nil
}

// ClickRow — одна строка POST /admin_api/v1/clicks/log.
type ClickRow struct {
	SubID          string     `json:"sub_id"`
	Datetime       string     `json:"datetime"`
	CampaignID     FlexInt    `json:"campaign_id"`
	Campaign       string     `json:"campaign"`
	StreamID       FlexInt    `json:"stream_id"`
	Stream         string     `json:"stream"`
	OfferID        FlexInt    `json:"offer_id"`
	LandingID      FlexInt    `json:"landing_id"`
	NetworkID      FlexInt    `json:"affiliate_network_id"`
	SourceID       FlexInt    `json:"ts_id"`
	CountryCode    string     `json:"country_code"`
	Region         string     `json:"region"`
	City           string     `json:"city"`
	Language       string     `json:"language"`
	OS             string     `json:"os"`
	OSVersion      string     `json:"os_version"`
	Browser        string     `json:"browser"`
	BrowserVersion string     `json:"browser_version"`
	DeviceType     string     `json:"device_type"`
	DeviceModel    string     `json:"device_model"`
	Operator       string     `json:"operator"`
	IsBot          FlexBool   `json:"is_bot"`
	IsUnique       FlexBool   `json:"is_unique_campaign"`
	IP             string     `json:"ip"`
	Referrer       string     `json:"referrer"`
	SearchEngine   string     `json:"search_engine"`
	Keyword        string     `json:"keyword"`
	UserAgent      string     `json:"user_agent"`
	Destination    string     `json:"destination"`
	Cost           FlexFloat  `json:"cost"`
	Revenue        FlexFloat  `json:"revenue"`
	AdCampaignID   FlexString `json:"ad_campaign_id"`
	ExternalID     FlexString `json:"external_id"`
	CreativeID     FlexString `json:"creative_id"`
	Subs           [12]string
}

// UnmarshalJSON читает строку клика и складывает sub_id_1..sub_id_12 в Subs.
//
// Параметры: data — JSON одной строки журнала.
// Результат: поля строки заполнены. Subs[0] — это sub_id_1.
// Ошибки: тело не объект или числовое поле не число и не числовая строка.
func (r *ClickRow) UnmarshalJSON(data []byte) error {
	var aux struct {
		SubID          string     `json:"sub_id"`
		Datetime       string     `json:"datetime"`
		CampaignID     FlexInt    `json:"campaign_id"`
		Campaign       string     `json:"campaign"`
		StreamID       FlexInt    `json:"stream_id"`
		Stream         string     `json:"stream"`
		OfferID        FlexInt    `json:"offer_id"`
		LandingID      FlexInt    `json:"landing_id"`
		NetworkID      FlexInt    `json:"affiliate_network_id"`
		SourceID       FlexInt    `json:"ts_id"`
		CountryCode    string     `json:"country_code"`
		Region         string     `json:"region"`
		City           string     `json:"city"`
		Language       string     `json:"language"`
		OS             string     `json:"os"`
		OSVersion      string     `json:"os_version"`
		Browser        string     `json:"browser"`
		BrowserVersion string     `json:"browser_version"`
		DeviceType     string     `json:"device_type"`
		DeviceModel    string     `json:"device_model"`
		Operator       string     `json:"operator"`
		IsBot          FlexBool   `json:"is_bot"`
		IsUnique       FlexBool   `json:"is_unique_campaign"`
		IP             string     `json:"ip"`
		Referrer       string     `json:"referrer"`
		SearchEngine   string     `json:"search_engine"`
		Keyword        string     `json:"keyword"`
		UserAgent      string     `json:"user_agent"`
		Destination    string     `json:"destination"`
		Cost           FlexFloat  `json:"cost"`
		Revenue        FlexFloat  `json:"revenue"`
		AdCampaignID   FlexString `json:"ad_campaign_id"`
		ExternalID     FlexString `json:"external_id"`
		CreativeID     FlexString `json:"creative_id"`
		logSubs
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*r = ClickRow{
		SubID: aux.SubID, Datetime: aux.Datetime, CampaignID: aux.CampaignID, Campaign: aux.Campaign,
		StreamID: aux.StreamID, Stream: aux.Stream, OfferID: aux.OfferID, LandingID: aux.LandingID,
		NetworkID: aux.NetworkID, SourceID: aux.SourceID, CountryCode: aux.CountryCode,
		Region: aux.Region, City: aux.City, Language: aux.Language, OS: aux.OS, OSVersion: aux.OSVersion,
		Browser: aux.Browser, BrowserVersion: aux.BrowserVersion, DeviceType: aux.DeviceType,
		DeviceModel: aux.DeviceModel, Operator: aux.Operator, IsBot: aux.IsBot, IsUnique: aux.IsUnique,
		IP: aux.IP, Referrer: aux.Referrer, SearchEngine: aux.SearchEngine, Keyword: aux.Keyword,
		UserAgent: aux.UserAgent, Destination: aux.Destination, Cost: aux.Cost, Revenue: aux.Revenue,
		AdCampaignID: aux.AdCampaignID, ExternalID: aux.ExternalID, CreativeID: aux.CreativeID,
		Subs: aux.pack(),
	}
	return nil
}

// logSubs — поля sub_id_1..sub_id_12 внутри строки журнала.
type logSubs struct {
	S1  string `json:"sub_id_1"`
	S2  string `json:"sub_id_2"`
	S3  string `json:"sub_id_3"`
	S4  string `json:"sub_id_4"`
	S5  string `json:"sub_id_5"`
	S6  string `json:"sub_id_6"`
	S7  string `json:"sub_id_7"`
	S8  string `json:"sub_id_8"`
	S9  string `json:"sub_id_9"`
	S10 string `json:"sub_id_10"`
	S11 string `json:"sub_id_11"`
	S12 string `json:"sub_id_12"`
}

// pack собирает двенадцать sub id в массив.
//
// Параметры: нет, читает поля получателя.
// Результат: индекс 0 соответствует sub_id_1.
// Ошибки: нет.
func (s logSubs) pack() [12]string {
	return [12]string{s.S1, s.S2, s.S3, s.S4, s.S5, s.S6, s.S7, s.S8, s.S9, s.S10, s.S11, s.S12}
}

// FlexString принимает JSON-строку или число и хранит текст.
// null, объект и массив становятся пустой строкой.
type FlexString string

// UnmarshalJSON читает строку из JSON-строки или числа.
//
// Параметры: data — JSON-значение.
// Результат: текст строки или числа как в JSON. null, объект, массив и нечисловой скаляр дают пустую строку.
// Ошибки: значение в кавычках не является JSON-строкой.
func (f *FlexString) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" || data[0] == '{' || data[0] == '[' {
		*f = ""
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("keitaro: строка не разобрана")
		}
		*f = FlexString(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err != nil {
		*f = ""
		return nil
	}
	*f = FlexString(n.String())
	return nil
}

// FlexInt принимает JSON-число или числовую строку.
type FlexInt int

// UnmarshalJSON читает целое из числа или десятичной строки.
//
// Параметры: data — JSON-значение.
// Результат: получатель равен числу. null и пустая строка дают 0.
// Ошибки: значение не число. Дробная часть отбрасывается.
func (f *FlexInt) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		*f = 0
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("keitaro: число не разобрано")
		}
		s = strings.TrimSpace(s)
		if s == "" {
			*f = 0
			return nil
		}
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf("keitaro: число не разобрано")
		}
		*f = FlexInt(n)
		return nil
	}
	var n float64
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("keitaro: число не разобрано")
	}
	*f = FlexInt(n)
	return nil
}

// FlexFloat принимает JSON-число или числовую строку.
// Объект и массив не роняют снимок: значение становится 0.
type FlexFloat float64

// UnmarshalJSON читает дробное из числа или строки.
//
// Параметры: data — JSON-значение.
// Результат: получатель равен числу. null, объект и массив дают 0.
// Ошибки: строка или скаляр, которые не являются числом.
func (f *FlexFloat) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" || data[0] == '{' || data[0] == '[' {
		*f = 0
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("keitaro: число не разобрано")
		}
		s = strings.TrimSpace(strings.ReplaceAll(s, ",", "."))
		if s == "" {
			*f = 0
			return nil
		}
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf("keitaro: число не разобрано")
		}
		*f = FlexFloat(n)
		return nil
	}
	var n float64
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("keitaro: число не разобрано")
	}
	*f = FlexFloat(n)
	return nil
}

// FlexBool принимает bool, 0/1 и строки true/false/yes/1.
type FlexBool bool

// UnmarshalJSON читает флаг из bool, числа или строки.
//
// Параметры: data — JSON-значение.
// Результат: true для true, 1, yes. Остальные понятные значения дают false.
// Ошибки: значение не похоже на флаг.
func (f *FlexBool) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		*f = false
		return nil
	}
	switch string(data) {
	case "true", "1":
		*f = true
		return nil
	case "false", "0":
		*f = false
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("keitaro: флаг не разобран")
		}
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "1", "true", "yes":
			*f = true
		default:
			*f = false
		}
		return nil
	}
	return fmt.Errorf("keitaro: флаг не разобран")
}

// FlexStrings принимает JSON-массив строк или одну строку со значениями через запятую.
type FlexStrings []string

// UnmarshalJSON читает список строк из массива или одной строки.
//
// Параметры: data — JSON-значение.
// Результат: список без пустых краёв. null даёт nil.
// Ошибки: значение не массив и не строка.
func (f *FlexStrings) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		*f = nil
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("keitaro: список не разобран")
		}
		*f = splitComma(s)
		return nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		*f = list
		return nil
	}
	var anyList []any
	if err := json.Unmarshal(data, &anyList); err != nil {
		return fmt.Errorf("keitaro: список не разобран")
	}
	out := make([]string, 0, len(anyList))
	for _, item := range anyList {
		out = append(out, strings.TrimSpace(fmt.Sprint(item)))
	}
	*f = out
	return nil
}

// splitComma режет строку по запятым и убирает пустые края.
//
// Параметры: raw — исходная строка.
// Результат: значения без пустых элементов. Пустая строка даёт nil.
// Ошибки: нет.
func splitComma(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	var out []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
