package path

import "path/filepath"

func PersonalChatDir() (string, error) {
	root, err := QuartetDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "personal-chat"), nil
}

func PersonalChatJournal() (string, error) {
	root, err := PersonalChatDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "commits.jsonl"), nil
}

func PersonalChatSessionDir(id string) (string, error) {
	root, err := PersonalChatDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "sessions", id), nil
}
