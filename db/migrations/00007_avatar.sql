-- +goose Up
-- Profile photo stored inline as a small data: URL (personal-scale; no object
-- store in the stack). Size is capped in the service layer.
ALTER TABLE users ADD COLUMN avatar_url text;

-- +goose Down
ALTER TABLE users DROP COLUMN avatar_url;
