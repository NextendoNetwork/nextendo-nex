package nex

// Lecture de l'identité jointe au login NEX : NON INCLUSE dans la version publique du cœur.
//
// La version de production lit ici la preuve d'identité et la version du jeu que le client
// joint à son login. Le dépôt public n'en garde que l'interface : rien n'est lu, et un serveur
// de jeu qui s'appuie sur ces fonctions voit toujours « aucune preuve ». Il lui revient alors
// de refuser le login.

// NexTokenFromLoginExtraData ne lit rien dans la version publique : ("", false).
func NexTokenFromLoginExtraData(extraData []byte) (string, bool) {
	return "", false
}

// TitleVersionFromLoginExtraData ne lit rien dans la version publique : ("", false).
func TitleVersionFromLoginExtraData(extraData []byte) (string, bool) {
	return "", false
}
