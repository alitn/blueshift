-- 0002_seed — idempotent baseline data. Safe to re-run: every insert is guarded
-- so re-applying is a no-op. No provider or model names appear in any value
-- (the vendor-leak gate greps this directory).

-- One pilot org. Guarded by NOT EXISTS since orgs has no natural unique key.
INSERT INTO orgs (name)
SELECT 'Blueshift Pilot'
WHERE NOT EXISTS (SELECT 1 FROM orgs WHERE name = 'Blueshift Pilot');

-- One show under the pilot org.
INSERT INTO shows (org_id, title)
SELECT o.id, 'Special Interviews'
FROM orgs o
WHERE o.name = 'Blueshift Pilot'
  AND NOT EXISTS (
      SELECT 1 FROM shows s WHERE s.org_id = o.id AND s.title = 'Special Interviews'
  );

-- No users or memberships are seeded here: this migration ships to every
-- environment, and user rows are dev/demo-only identities. Dev users live in
-- fixtures/dev-seed.sql (applied by `make dev-seed`); staging/prod provisioning
-- follows docs/RUNBOOK.md.

-- Global config: self-approval stays on until the M2 roles split.
INSERT INTO config (org_id, key, value)
VALUES (NULL, 'allow_self_approval', 'true'::jsonb)
ON CONFLICT (org_id, key) DO NOTHING;

-- Global config: platform presets (config rows, not code). Neutral field names;
-- no provider or model names.
INSERT INTO config (org_id, key, value)
VALUES (NULL, 'platform_presets', '[
    {
        "id": "reels",
        "label": "Reels",
        "width": 1080,
        "height": 1920,
        "video_codec": "h264",
        "video_profile": "high",
        "loudness_lufs": -14,
        "burn_captions": true
    },
    {
        "id": "telegram",
        "label": "Telegram",
        "width": 720,
        "height": 1280,
        "video_codec": "h264",
        "crf": 23,
        "burn_captions": true
    }
]'::jsonb)
ON CONFLICT (org_id, key) DO NOTHING;
