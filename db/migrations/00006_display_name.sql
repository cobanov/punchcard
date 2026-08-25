-- +goose Up
ALTER TABLE users ADD COLUMN display_name text;

-- +goose Down
ALTER TABLE users DROP COLUMN display_name;
