package nex

// NEX result codes (subset used by this stack). A code with the high bit set is
// an error; the same numeric value with the bit clear is its success flavor
// (e.g. login success is returned as ResultCoreUnknown without the error bit).
const (
	ResultCoreUnknown                 uint32 = 0x00010001
	ResultCoreNotImplemented          uint32 = 0x00010002
	ResultCoreAccessDenied            uint32 = 0x00010006
	ResultCoreInvalidArgument         uint32 = 0x0001000A
	ResultRendezVousInvalidPID        uint32 = 0x0003006B
	ResultRendezVousSessionVoid       uint32 = 0x00030073
	ResultRendezVousSessionFull       uint32 = 0x000300C8
	ResultRendezVousPermissionDenied  uint32 = 0x000300D9
	ResultAuthTokenParseError         uint32 = 0x00680002
	ResultAuthUnsupportedVersion      uint32 = 0x0068000C
	ResultAuthUnknown                 uint32 = 0x0068000E
	ResultAuthClientVersionIsOld      uint32 = 0x0068000F
	ResultAuthApplicationVersionIsOld uint32 = 0x00680013
)

// ResultRendezVousUserPasswordUnmatch (MatchmakeSessionUserPasswordUnmatch) : mot de passe
// d'arene refuse a la jonction (0x6D.0x27). C'est le SEUL code que Smash 13.0.5 traduit en
// « Le code d'acces est incorrect. » : la table de fin de jonction de Pia (0x449563c, case
// 0x800300D5 en 0xdf27c) le change en 0x6c51, que 0xbdf50 rend comme 2618-0545, que Smash
// compare en 0x16a85fc/0x16a8608. InvalidGatheringPassword (0x800300C9) tombe dans le cas par
// defaut de la meme table : fenetre generique, pas celle du mot de passe.
const ResultRendezVousUserPasswordUnmatch uint32 = 0x000300D5

// ResultRendezVousInvalidGatheringPassword (0x800300C9) : jonction d'arene refusee SANS
// comparaison du mot de passe, essais freines (mdp_essais.go). Trace dans le jeu
// (un outil interne, B1 a B5 et C) : cas par defaut de la table
// 0x449563c (0xdf088) ; 0x1b1200 en tire l'ErrorCode 2306-0323 (code annonce au joueur),
// identifiant -0x3d9 (le meme que 2618-0516), fenetre de type 0x1c
// pop_online_room_not_enter, « Impossible de rejoindre cette arene. ». Rien sur le mot de
// passe : ni le joueur qui a tape le bon, ni celui qui cherche, n'apprend s'il etait juste.
const ResultRendezVousInvalidGatheringPassword uint32 = 0x000300C9
