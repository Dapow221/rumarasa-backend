-- The facilities section was replaced by a static venue section driven by
-- content blocks, so its collection is no longer served.
DROP TABLE IF EXISTS facilities;

-- Happenings now describe the kinds of events the venue hosts, each with its
-- own photo, so the seeded schedule-based entries are replaced.
DELETE FROM happenings;

INSERT INTO happenings (title, description, sort) VALUES
    ('Family Gathering', 'Nikmati minuman dan makanan lezat ala Rumarasa Nusantara bersama keluarga tercinta dalam nuansa asri yang menenangkan.', 1),
    ('Arisan', 'Rasakan sentuhan seni kuliner Rumarasa Nusantara, di mana cita rasa tradisional bertemu presentasi modern.', 2),
    ('Komunitas', 'Tempat yang pas untuk berkumpul bersama komunitas — berbagi cerita, ide, dan cita rasa Nusantara.', 3),
    ('Ulang Tahun', 'Buat momen ulang tahun Anda lebih spesial dengan suasana hangat dan menu istimewa dari dapur kami.', 4),
    ('Wedding', 'Rayakan hari istimewa Anda di tempat yang ideal untuk resepsi pernikahan yang elegan dan berkesan.', 5),
    ('Korporat', 'Pilihan tepat untuk acara korporat dengan suasana eksklusif dan hidangan berkualitas.', 6);
