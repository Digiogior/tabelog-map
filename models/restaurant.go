package models

type Restaurant struct {
	Name           string
	Alias          string
	PillowWord     string
	Address        string
	URL            string
	Rating         float64
	Latitude       float64
	Longitude      float64
	LunchMinPrice  int
	LunchMaxPrice  int
	DinnerMinPrice int
	DinnerMaxPrice int
	NearestStation string
	Categories     []string
	City           string
	Kids           string
}
