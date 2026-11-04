
ALTER TABLE gb_entity_geo ADD COLUMN IF NOT EXISTS geo %[1]s.geography(Point, 4326)
	GENERATED ALWAYS AS (%[1]s.ST_SetSRID(%[1]s.ST_MakePoint(longitude, latitude), 4326)::%[1]s.geography) STORED;
CREATE INDEX IF NOT EXISTS idx_gb_entity_geo_gist ON gb_entity_geo USING GIST (geo);