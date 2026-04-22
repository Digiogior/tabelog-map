package models

type MenuItem struct {
	RestaurantID int64
	Name         string
	Price        *int
	PriceBottle  *int
	Description  *string
}

type MenuSearchResult struct {
	RestaurantName    string  `json:"restaurant_name"`
	RestaurantAddress string  `json:"restaurant_address"`
	RestaurantURL     string  `json:"restaurant_url"`
	RestaurantRating  float64 `json:"restaurant_rating"`
	Latitude          float64 `json:"latitude"`
	Longitude         float64 `json:"longitude"`
	ItemName          string  `json:"item_name"`
	Price             *int    `json:"price"`
	Description       *string `json:"description"`
}
