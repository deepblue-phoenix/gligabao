
-- The geo index: an R*Tree with one box per geo entity (a point: min = max), id = entity_id, so that proximity queries can
-- ask for the entities in a bounding box first (WHERE entity_id IN (SELECT entity_id FROM gb_entity_geo_rtree WHERE min_lat >= ?
-- AND max_lat <= ? AND ...)) and only then compute exact distances. The triggers keep it in step with gb_entity_geo however
-- the row is written or removed (the upsert of a place, or the cascade of a deleted entity). Rows stored before the index
-- existed are not indexed: there is no backfill.
CREATE VIRTUAL TABLE IF NOT EXISTS gb_entity_geo_rtree USING rtree(entity_id, min_lat, max_lat, min_lon, max_lon);
CREATE TRIGGER IF NOT EXISTS trg_gb_entity_geo_rtree_ins AFTER INSERT ON gb_entity_geo BEGIN
	INSERT INTO gb_entity_geo_rtree VALUES (NEW.entity_id, NEW.latitude, NEW.latitude, NEW.longitude, NEW.longitude);
END;
CREATE TRIGGER IF NOT EXISTS trg_gb_entity_geo_rtree_upd AFTER UPDATE OF latitude, longitude ON gb_entity_geo BEGIN
	DELETE FROM gb_entity_geo_rtree WHERE entity_id = OLD.entity_id;
	INSERT INTO gb_entity_geo_rtree VALUES (NEW.entity_id, NEW.latitude, NEW.latitude, NEW.longitude, NEW.longitude);
END;
CREATE TRIGGER IF NOT EXISTS trg_gb_entity_geo_rtree_del AFTER DELETE ON gb_entity_geo BEGIN
	DELETE FROM gb_entity_geo_rtree WHERE entity_id = OLD.entity_id;
END;