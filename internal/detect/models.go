package detect

import "strings"

// modelNames maps an upper-case device token to one trade name.
// A hardware code and the marketing words of the same phone share one value,
// so the click log does not split them. A code that is not here is kept as read.
// SM-A075F is the Galaxy A07 and SM-A175F is the Galaxy A17; neither is the
// Galaxy A05 (SM-A055F), and both stay under the code the report already shows.
// SHV48 is Sharp's AQUOS sense3 basic, which catalogues list as Aquos S3 Basic.
// The Redmi Note 12 5G (22111317G and its siblings) is a different phone and is
// not in this map.
var modelNames = map[string]string{
	"SM-A055F": "Samsung Galaxy A05", "SM-A055M": "Samsung Galaxy A05",
	"GALAXY A05": "Samsung Galaxy A05",

	"SM-A135F": "Samsung Galaxy A13", "SM-A135M": "Samsung Galaxy A13",
	"SM-A135U": "Samsung Galaxy A13", "SM-A135U1": "Samsung Galaxy A13",
	"SM-A137F": "Samsung Galaxy A13",
	"GALAXY A13": "Samsung Galaxy A13",

	"SM-A145F": "Samsung Galaxy A14", "SM-A145M": "Samsung Galaxy A14",
	"SM-A145P": "Samsung Galaxy A14", "SM-A145R": "Samsung Galaxy A14",
	"GALAXY A14": "Samsung Galaxy A14",

	"SM-A165F": "Samsung Galaxy A16", "SM-A165M": "Samsung Galaxy A16",
	"GALAXY A16": "Samsung Galaxy A16",

	"SM-A166B": "Samsung Galaxy A16 5G", "SM-A166E": "Samsung Galaxy A16 5G",
	"SM-A166P": "Samsung Galaxy A16 5G", "SM-A166U": "Samsung Galaxy A16 5G",
	"SM-A166U1": "Samsung Galaxy A16 5G",
	"GALAXY A16 5G": "Samsung Galaxy A16 5G",

	"SM-A075F": "Samsung SM-A075F",
	"SM-A175F": "Samsung SM-A175F",

	"2409BRN2CA": "Xiaomi Redmi 14C", "2409BRN2CG": "Xiaomi Redmi 14C",
	"2409BRN2CY": "Xiaomi Redmi 14C", "2411DRN47C": "Xiaomi Redmi 14C",
	"REDMI 14C": "Xiaomi Redmi 14C",

	"23021RAAEG": "Xiaomi Redmi Note 12", "23021RAA2Y": "Xiaomi Redmi Note 12",
	"23021RAAEI": "Xiaomi Redmi Note 12", "23027RAD4I": "Xiaomi Redmi Note 12",
	"23028RA60L": "Xiaomi Redmi Note 12",
	"REDMI NOTE 12":     "Xiaomi Redmi Note 12",
	"REDMI NOTE 12 4G":  "Xiaomi Redmi Note 12",
	"REDMI NOTE 12 NFC": "Xiaomi Redmi Note 12",

	"ABR-LX1": "Huawei Honor X8c", "ABR-LX2": "Huawei Honor X8c",
	"ABR-LX3": "Huawei Honor X8c", "HONOR X8C": "Huawei Honor X8c",

	"LLY-LX1": "Huawei Honor X8b", "LLY-LX2": "Huawei Honor X8b",
	"LLY-LX3": "Huawei Honor X8b", "HONOR X8B": "Huawei Honor X8b",

	"DNY-NX9": "Huawei Honor 400", "HONOR 400": "Huawei Honor 400",

	"HUAWEI": "Huawei",

	"REVVL V+ 5G": "T-Mobile REVVL V+ 5G",

	"X6880": "Infinix Hot 50 Pro+", "INFINIX HOT 50 PRO+": "Infinix Hot 50 Pro+",
	"HOT 50 PRO+": "Infinix Hot 50 Pro+",

	"CG7": "Tecno Mobile Camon 17P", "CG7N": "Tecno Mobile Camon 17P",
	"CAMON 17P": "Tecno Mobile Camon 17P",

	"RMX3842": "Realme 12 Pro 5G", "REALME 12 PRO 5G": "Realme 12 Pro 5G",

	"SHV48": "Sharp Aquos S3 Basic", "AQUOS S3 BASIC": "Sharp Aquos S3 Basic",
	"AQUOS SENSE3 BASIC": "Sharp Aquos S3 Basic",
}

// tradeModel returns the trade name for a cleaned device token.
//
// s is the token modelOf or HintModel already trimmed. The lookup is exact on
// the upper-case token, then on the same token without its first word, so
// "SAMSUNG SM-A055F" and "SM-A055F" share one name. It returns "" when s is
// empty or not in modelNames; the caller then keeps s. It does not fail.
func tradeModel(s string) string {
	key := strings.ToUpper(s)
	if name, ok := modelNames[key]; ok {
		return name
	}
	if i := strings.IndexByte(key, ' '); i > 0 {
		if name, ok := modelNames[key[i+1:]]; ok {
			return name
		}
	}
	return ""
}
