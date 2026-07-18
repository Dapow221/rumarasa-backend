-- Admin accounts (no public registration — bootstrapped from env at startup)
CREATE TABLE admins (
    id            serial PRIMARY KEY,
    username      varchar(100) NOT NULL UNIQUE,
    password_hash text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- Opaque refresh tokens, stored hashed, rotated on every use
CREATE TABLE refresh_tokens (
    token_hash text PRIMARY KEY,
    admin_id   integer NOT NULL REFERENCES admins (id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX refresh_tokens_admin_id_idx ON refresh_tokens (admin_id);

-- Free-form editable text blocks: every click-to-edit string on the site
-- lives here under a stable key, e.g. 'site.tagline' or 'hero.description'.
CREATE TABLE content_blocks (
    key        varchar(200) PRIMARY KEY CHECK (key ~ '^[a-z0-9][a-z0-9._-]*$'),
    value      text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Images stored directly in Postgres, served via GET /api/v1/images/{id}
CREATE TABLE images (
    id           text PRIMARY KEY,
    filename     varchar(255) NOT NULL,
    content_type varchar(100) NOT NULL,
    size_bytes   integer NOT NULL,
    data         bytea NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- Collections backing the landing page sections
CREATE TABLE dishes (
    id          serial PRIMARY KEY,
    kind        varchar(20) NOT NULL CHECK (kind IN ('food', 'beverage')),
    name        varchar(200) NOT NULL,
    price       varchar(50) NOT NULL DEFAULT '',
    description text NOT NULL DEFAULT '',
    image_url   text NOT NULL DEFAULT '',
    sort        integer NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE promos (
    id             serial PRIMARY KEY,
    badge          varchar(100) NOT NULL DEFAULT '',
    title          varchar(200) NOT NULL,
    description    text NOT NULL DEFAULT '',
    description_en text NOT NULL DEFAULT '',
    price          varchar(50) NOT NULL DEFAULT '',
    price_note     varchar(100) NOT NULL DEFAULT '',
    image_url      text NOT NULL DEFAULT '',
    featured       boolean NOT NULL DEFAULT false,
    sort           integer NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE happenings (
    id          serial PRIMARY KEY,
    schedule    varchar(200) NOT NULL DEFAULT '',
    title       varchar(200) NOT NULL,
    description text NOT NULL DEFAULT '',
    image_url   text NOT NULL DEFAULT '',
    sort        integer NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE facilities (
    id             serial PRIMARY KEY,
    title          varchar(200) NOT NULL,
    description    text NOT NULL DEFAULT '',
    description_en text NOT NULL DEFAULT '',
    image_url      text NOT NULL DEFAULT '',
    sort           integer NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE member_benefits (
    id          serial PRIMARY KEY,
    highlight   varchar(200) NOT NULL,
    description text NOT NULL DEFAULT '',
    sort        integer NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- ============================================================
-- Seed: current content of the Next.js landing page (lib/site.ts, lib/content.ts)
-- ============================================================

INSERT INTO content_blocks (key, value) VALUES
    ('site.name', 'Rumarasa Nusantara'),
    ('site.tagline', 'Taste of Authenticity'),
    ('site.description', 'Cita rasa Nusantara di jantung Jakarta Selatan. The archipelago''s authentic flavors, served in the heart of South Jakarta.'),
    ('site.whatsapp_number', '6281234567890'),
    ('site.phone', '+62215550123'),
    ('site.address_street', 'Jl. Kemang Raya No. 88'),
    ('site.address_city', 'Jakarta Selatan 12730'),
    ('site.hours_weekday_days', 'Sen – Jum'),
    ('site.hours_weekday_time', '11.00 – 22.00 WIB'),
    ('site.hours_weekend_days', 'Sab – Min'),
    ('site.hours_weekend_time', '10.00 – 23.00 WIB'),
    ('site.link_maps', 'https://maps.google.com/?q=Rumarasa+Nusantara+Jakarta+Selatan'),
    ('site.link_instagram', 'https://www.instagram.com/rumarasa.nusantara/'),
    ('site.link_tiktok', 'https://www.tiktok.com/@rumarasa.nusantara');

INSERT INTO dishes (kind, name, price, description, sort) VALUES
    ('food', 'Rendang Sapi Padang', 'Rp 78.000', 'Daging sapi dimasak 8 jam dalam santan dan rempah Minang hingga empuk dan pekat bumbunya.', 1),
    ('food', 'Ayam Betutu Bali', 'Rp 72.000', 'Ayam utuh berbumbu base genep khas Bali, dipanggang perlahan dalam balutan daun pisang.', 2),
    ('food', 'Ikan Bakar Jimbaran', 'Rp 85.000', 'Ikan segar bakar sambal matah, disajikan dengan plecing kangkung dan nasi hangat.', 3),
    ('food', 'Gudeg Yogya Komplit', 'Rp 62.000', 'Nangka muda manis gurih dengan krecek, telur pindang, dan ayam kampung opor.', 4),
    ('food', 'Coto Makassar', 'Rp 58.000', 'Sup daging kaya rempah dari Sulawesi Selatan, disajikan dengan ketupat dan sambal tauco.', 5),
    ('food', 'Sate Lilit Bali', 'Rp 55.000', 'Sate ikan cincang berbumbu, dililit pada batang serai dan dibakar di atas arang.', 6),
    ('beverage', 'Es Cendol Gula Aren', 'Rp 32.000', 'Cendol pandan lembut, santan segar, dan gula aren asli — penutup yang menyejukkan.', 1),
    ('beverage', 'Es Teler Nusantara', 'Rp 35.000', 'Alpukat, kelapa muda, nangka, dan cincau dalam kuah santan susu yang segar.', 2),
    ('beverage', 'Kopi Tubruk Gayo', 'Rp 28.000', 'Kopi arabika Gayo diseduh tubruk khas warung kopi tempo dulu.', 3),
    ('beverage', 'Wedang Jahe Sereh', 'Rp 25.000', 'Jahe bakar, sereh, dan gula aren — hangat dan menenangkan.', 4),
    ('beverage', 'Es Kelapa Muda Jeruk', 'Rp 30.000', 'Kelapa muda utuh dengan perasan jeruk nipis dan gula aren cair.', 5),
    ('beverage', 'Jus Alpukat Kopi', 'Rp 33.000', 'Alpukat mentega lembut dengan lelehan kopi susu gula aren.', 6);

INSERT INTO promos (badge, title, description, description_en, price, price_note, featured, sort) VALUES
    ('Senin – Kamis', 'Paket Makan Siang', 'Nasi, lauk pilihan, sayur, dan es teh — harga khusus jam 11.00–14.00.', 'Weekday lunch set.', 'Rp 65.000', '/ orang', false, 1),
    ('Akhir pekan', 'Rijsttafel Keluarga', 'Sajian 8 hidangan Nusantara untuk 4–6 orang, gratis dessert sampler.', 'Weekend family feast.', 'Rp 480.000', '/ meja', true, 2),
    ('Member', 'Kopi & Kudapan Sore', 'Diskon 20% kopi Nusantara & jajanan pasar setiap hari, 15.00–17.30.', 'Member afternoon treat.', '−20%', 'khusus member', false, 3);

INSERT INTO happenings (schedule, title, description, sort) VALUES
    ('Setiap Jumat malam', 'Musik Akustik Live', 'Lagu-lagu daerah dan tembang kenangan mengiringi makan malam Anda. Live acoustic every Friday.', 1),
    ('Sabtu, 2× sebulan', 'Kelas Masak Nusantara', 'Belajar meracik bumbu dan memasak hidangan klasik bersama chef kami. Hands-on cooking class.', 2),
    ('Privat & korporat', 'Ruang Acara Privat', 'Arisan, ulang tahun, hingga gathering kantor hingga 60 tamu. Private dining up to 60 guests.', 3);

INSERT INTO facilities (title, description, description_en, sort) VALUES
    ('Area Outdoor', 'Teras rindang untuk bersantap sore di udara terbuka.', 'Garden terrace.', 1),
    ('Ruang Privat', 'Ruang VIP ber-AC untuk acara keluarga dan kantor.', 'Private VIP room.', 2),
    ('Musala', 'Musala bersih dengan perlengkapan salat lengkap.', 'Prayer room.', 3),
    ('Parkir Luas', 'Parkir mobil dan motor yang luas, gratis untuk tamu.', 'Ample free parking.', 4);

INSERT INTO member_benefits (highlight, description, sort) VALUES
    ('Poin setiap transaksi', 'tukarkan dengan hidangan favorit Anda.', 1),
    ('Diskon ulang tahun 25%', 'untuk Anda dan keluarga.', 2),
    ('Akses awal', 'ke menu musiman, kelas masak, dan acara spesial.', 3);
