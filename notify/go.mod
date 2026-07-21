module github.com/psimaker/vaultsync/notify

go 1.26.5

require github.com/gobwas/glob v0.2.3

// Match the exact glob implementation used by the bundled Syncthing version.
replace github.com/gobwas/glob v0.2.3 => github.com/calmh/glob v0.0.0-20220615080505-1d823af5017b
