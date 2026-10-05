-- The facilities section is back on the landing page, alongside (not instead
-- of) the venue section that replaced it in 0002. Recreate the table with the
-- original shape and reseed it with the amenities the site falls back to.
CREATE TABLE IF NOT EXISTS facilities (
    id             serial PRIMARY KEY,
    title          varchar(200) NOT NULL,
    description    text NOT NULL DEFAULT '',
    description_en text NOT NULL DEFAULT '',
    image_url      text NOT NULL DEFAULT '',
    sort           integer NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

INSERT INTO facilities (title, description, description_en, sort) VALUES
    ('Area Outdoor', 'Teras rindang untuk bersantap sore di udara terbuka.', 'Garden terrace.', 1),
    ('Ruang Privat', 'Ruang VIP ber-AC untuk acara keluarga dan kantor.', 'Private VIP room.', 2),
    ('Musala', 'Musala bersih dengan perlengkapan salat lengkap.', 'Prayer room.', 3),
    ('Parkir Luas', 'Parkir mobil dan motor yang luas, gratis untuk tamu.', 'Ample free parking.', 4);
