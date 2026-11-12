-- The relations the connectors beside this file read (ADR 0020), as views
-- over a synthetic parts system's and yard system's own tables. nitrovin
-- dictates the views' column names, never their SQL: each system keeps its
-- tables, and a view renames. pkg/stock/pg's TestParityPostgres runs this
-- file, fills the tables with the rows of testdata/vocab/stock's files, and
-- answers fit from both. docs/vocabularies.md (Connectors) walks through it.

-- A parts system: two shelves in one table, and the catalog release each
-- is numbered under.
CREATE SCHEMA parts;
CREATE TABLE parts.shelf (
    shelf       text NOT NULL,             -- 'main' or 'annex'
    stock_id    text PRIMARY KEY,
    bin         text NOT NULL,
    part_class  text,                      -- the fitment key, for a numbered part
    catalog_no  text,
    hand        text CHECK (hand IN ('left', 'right', 'both')),
    spine_part  text,                      -- the part type, for a part with no number
    make        text,                      -- and its vehicle and year
    model       text,
    model_year  integer,
    attrs       jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX shelf_by_number ON parts.shelf (part_class, catalog_no, hand);
CREATE INDEX shelf_by_vehicle ON parts.shelf (spine_part, make, model, model_year) WHERE catalog_no IS NULL;
CREATE TABLE parts.release (
    shelf   text PRIMARY KEY,
    release text NOT NULL
);

-- A yard system: whole vehicles, each with the vPIC decode it computed
-- for its VIN (by nitrovin's batch decode) and its own make and model.
CREATE SCHEMA yard;
CREATE TABLE yard.vehicles (
    stock_id   text PRIMARY KEY,
    lot        text NOT NULL,
    vpic_make  integer,
    vpic_model integer,
    vpic_year  integer,
    make       text,
    model      text,
    model_year integer,
    attrs      jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX yard_by_model ON yard.vehicles (vpic_model, vpic_year);
CREATE INDEX yard_by_vehicle ON yard.vehicles (make, model, model_year);

-- The views. A shelf has type, number and side (null with no number),
-- part_type, vehicle_<field> per field of the catalog's vehicle
-- vocabulary (catalog-models: make, model), year and fields; a yard has
-- decoded_make, decoded_model and decoded_year in place of the shelf's
-- part. Either may add as_of, a row's own time.
CREATE SCHEMA stock;
CREATE VIEW stock.shelf_stock AS
SELECT stock_id AS id, bin AS location,
       part_class AS type, catalog_no AS number, hand AS side, spine_part AS part_type,
       make AS vehicle_make, model AS vehicle_model, model_year AS year,
       attrs AS fields
FROM parts.shelf WHERE shelf = 'main';
CREATE VIEW stock.shelf_stock_release AS
SELECT release FROM parts.release WHERE shelf = 'main';
CREATE VIEW stock.shelf_annex AS
SELECT stock_id AS id, bin AS location,
       part_class AS type, catalog_no AS number, hand AS side, spine_part AS part_type,
       make AS vehicle_make, model AS vehicle_model, model_year AS year,
       attrs AS fields
FROM parts.shelf WHERE shelf = 'annex';
CREATE VIEW stock.yard_stock AS
SELECT stock_id AS id, lot AS location,
       vpic_make AS decoded_make, vpic_model AS decoded_model, vpic_year AS decoded_year,
       make AS vehicle_make, model AS vehicle_model, model_year AS year,
       attrs AS fields
FROM yard.vehicles;

-- The connection's user reads the views and nothing else.
CREATE ROLE nitrovin_stock LOGIN PASSWORD 'stock-reader';
GRANT USAGE ON SCHEMA stock TO nitrovin_stock;
GRANT SELECT ON ALL TABLES IN SCHEMA stock TO nitrovin_stock;
