package admin

import (
	"fmt"
	"strings"
)

// aiResourceNames names the resources of the permissions in each language, as the panel calls its
// sections. A resource or a language missing here is shown by its own key.
var aiResourceNames = map[string]map[string]string{
	"en": {"campaigns": "Campaigns", "offers": "Offers", "landings": "Landings",
		"affiliate_networks": "Networks", "traffic_sources": "Sources", "domains": "Domains"},
	"ru": {"campaigns": "Кампании", "offers": "Офферы", "landings": "Лендинги",
		"affiliate_networks": "Сети", "traffic_sources": "Источники", "domains": "Домены"},
	"uk": {"campaigns": "Кампанії", "offers": "Оффери", "landings": "Лендінги",
		"affiliate_networks": "Мережі", "traffic_sources": "Джерела", "domains": "Домени"},
}

// aiDeniedReadOnly and aiDeniedNoDelete are the sentences that tell the user a change is refused,
// by language. The first takes the section name: nothing there may be changed, reading is allowed.
// The second takes it too: deleting is refused, reading and editing are allowed.
var (
	aiDeniedReadOnly = map[string]string{
		"en": "I cannot change the section «%s»: only reading is allowed there by the assistant permissions or your access. I can read it.",
		"ru": "Раздел «%s» менять нельзя: права ассистента или ваш доступ разрешают там только чтение. Читать можно.",
		"uk": "Розділ «%s» змінювати не можна: права асистента або ваш доступ дозволяють там лише читання. Читати можна.",
	}
	aiDeniedNoDelete = map[string]string{
		"en": "I cannot delete records of the section «%s»: deleting is not allowed by the assistant permissions. I can read and edit it.",
		"ru": "Удалять записи раздела «%s» нельзя: права ассистента не разрешают удаление. Читать и править можно.",
		"uk": "Видаляти записи розділу «%s» не можна: права асистента не дозволяють видалення. Читати й змінювати можна.",
	}
	aiDeniedNote = map[string]string{
		"en": "The assistant may only read: %s. Changing them is not allowed; if asked to change one, say so plainly and offer to read it.",
		"ru": "Ассистенту разрешено только читать: %s. Менять их нельзя; если просят изменить, скажи об этом прямо и предложи прочитать.",
		"uk": "Асистентові дозволено лише читати: %s. Змінювати їх не можна; якщо просять змінити, скажи про це прямо й запропонуй прочитати.",
	}
)

// aiResourceName returns the name of res in lang, or res itself.
func aiResourceName(lang, res string) string {
	if name, ok := aiResourceNames[aiLangOf(lang)][res]; ok {
		return name
	}
	return res
}

// aiDeniedText returns the sentence for the user when a call of operation op ("" for a read) on
// resource res was refused. level is the level the assistant permissions give res. A delete of a
// resource the assistant may edit is a refusal of deleting only; every other refused change means
// the resource is read-only for this turn, by the assistant permissions or by the access of the user.
// It returns "" for a read, which is not a change.
func aiDeniedText(lang, res, op, level string) string {
	if op == "" {
		return ""
	}
	lang = aiLangOf(lang)
	table := aiDeniedReadOnly
	if op == aiOpDelete && aiPermRank[level] >= aiPermRank[aiPermEdit] {
		table = aiDeniedNoDelete
	}
	return fmt.Sprintf(table[lang], aiResourceName(lang, res))
}

// aiReadOnlyNote returns the part of the system prompt that lists the resources the assistant may
// only read under p, in lang, or "" when it may change every one of them. Domains are not listed:
// the assistant has no domain write tools at all.
func aiReadOnlyNote(lang string, p aiPerms) string {
	var names []string
	for _, r := range aiPermResources {
		if r.Name != "domains" && p.level(r.Name) == aiPermRead {
			names = append(names, aiResourceName(lang, r.Name))
		}
	}
	if len(names) == 0 {
		return ""
	}
	return fmt.Sprintf(aiDeniedNote[aiLangOf(lang)], strings.Join(names, ", "))
}
