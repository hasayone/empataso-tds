package ktimport

// mapCtx держит справочники одного прогона маппера кампаний.
//
// offers и landings — URL по id Keitaro. groups — папки по id группы.
// aliases — id кампании → id группы. seen — уже выданные id групп.
// swaps — placeholder текущей кампании → маска emtds. lists и pages — файлы этого прогона.
// losses — отчёт текущей кампании, пока собираются её потоки.
// only — id кампании Keitaro, которую надо перенести, или 0 для всех.
type mapCtx struct {
	offers   map[int]Offer
	landings map[int]Landing
	groups   map[int][]KGroup
	aliases  map[int]string
	seen     map[string]bool
	swaps    map[string]string
	lists    *listBag
	pages    *pageBag
	losses   *[]Loss
	only     int
}
