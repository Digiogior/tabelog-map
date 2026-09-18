package models

type Restaurant struct {
	Name             string
	NameJa           string
	Alias            string
	AliasJa          string
	PillowWord       string
	PillowWordJa     string
	Address          string
	AddressJa        string
	URL              string
	Rating           float64
	Latitude         float64
	Longitude        float64
	LunchMinPrice    int
	LunchMaxPrice    int
	DinnerMinPrice   int
	DinnerMaxPrice   int
	NearestStation   string
	NearestStationJa string
	Categories       []string
	CategoriesJa     []string
	City             string
	Kids             string
	KidsJa           string
	GoodForKids      bool
	Photos           []string
}
