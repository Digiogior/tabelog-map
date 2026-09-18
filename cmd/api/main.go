package main

import (
	"database/sql"
	"log"
	"os"
	"tabelog-map/internal/api"
	"tabelog-map/internal/db"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://postgres:password@localhost:5432/nagoya"
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	if err := conn.Ping(); err != nil {
		log.Fatal("failed to connect to postgres:", err)
	}
	if err := db.CreateMenuTables(conn); err != nil {
		log.Fatal("failed to initialize menu tables:", err)
	}

	api.StartServer(conn)
}
