# 使用官方 arm64v8 Postgres 18 映像檔
FROM arm64v8/postgres:18

# 更新並安裝 PostGIS 擴充套件
# 註：PostgreSQL 18 通常搭配 PostGIS 3
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
       postgresql-18-postgis-3 \
       postgresql-18-postgis-3-scripts \
    && rm -rf /var/lib/apt/lists/*

# (選填) 如果需要自動初始化擴充套件，可以放一個腳本到 initdb.d
RUN echo "CREATE EXTENSION IF NOT EXISTS postgis;" > /docker-entrypoint-initdb.d/postgis.sql