package extract

import "testing"

func TestJoinableEnvNames(t *testing.T) {
	for name, want := range map[string]bool{
		"ADAPTER_CLONE_TIMEOUT": true, "STRIPE_WEBHOOK_SECRET": true, "X_Y_Z": true,
		"APP_NAME": false, "DB_HOST": false, "REDIS_URL": false, "LOG_CHANNEL": false, "VITE_API_BASE": false, "MIX_URL": false,
		"AWS_REGION": false, "MAIL_FROM": false, "QUEUE_CONNECTION": false, "BROADCAST_DRIVER": false,
		"PORT": false, "HOST": false, "NODE_ENV": false, "DEBUG": false, "GOPATH": false, "PATH": false,
		"MEMCACHED_HOST": false, "SQS_QUEUE": false, "POSTMARK_TOKEN": false, "DYNAMODB_ENDPOINT": false, "PAPERTRAIL_URL": false,
		"BCRYPT_ROUNDS": false, "RESEND_KEY": false, "SLACK_WEBHOOK_URL": false, "MAILGUN_DOMAIN": false, "SES_REGION": false,
		"PUSHER_APP_KEY": false, "REVERB_PORT": false, "OCTANE_HTTPS": false, "TELESCOPE_ENABLED": false, "SANCTUM_STATEFUL_DOMAINS": false,
		"XDEBUG_MODE": false, "PHP_INI_SCAN_DIR": false, "NPM_CONFIG_CACHE": false, "YARN_CACHE_FOLDER": false, "DOCKER_HOST": false,
		"COMPOSE_PROJECT_NAME": false, "POSTGRES_DB": false, "MYSQL_USER": false, "MARIADB_ROOT_PASSWORD": false, "GITHUB_TOKEN": false,
		"KUBERNETES_SERVICE_HOST": false, "SESSION_DRIVER": false, "SESAME_KEY": true, "SQSX_QUEUE": true, "MEMCACHE_HOST": true,
		"TOKEN": false, "A_B": false, "ABC_": true, "ADAPTER_timeout": false, "ADAPTER-TIMEOUT": false, "": false,
	} {
		if got := joinableEnv(name); got != want {
			t.Errorf("joinableEnv(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestImageRepositoryNormalisation(t *testing.T) {
	for ref, want := range map[string]string{
		"ghcr.io/org/app-backend:latest":                     "org/app-backend",
		"registry.example.com/org/app-backend:1.2":           "org/app-backend",
		"registry.example.com/org/app-backend@sha256:abcdef": "org/app-backend",
		"localhost:5000/org/app":                             "org/app",
		"org/app":                                            "org/app",
		"org/app:1.2.3":                                      "org/app",
		"library/postgres:16":                                "library/postgres",
		"postgres:16":                                        "",
		"nginx":                                              "",
		"scratch":                                            "",
		"ghcr.io/org/app:${{ github.sha }}":                  "",
		"ghcr.io/org/${IMAGE}:latest":                        "",
		"assets/logo.png":                                    "",
		"assets/logo.PNG":                                    "",
		"config/values.Yaml":                                 "",
		"assets/data.JSON":                                   "",
		"assets/Icon.Svg":                                    "",
		"":                                                   "",
		"/app":                                               "",
	} {
		if got := imageRepository(ref); got != want {
			t.Errorf("imageRepository(%q) = %q, want %q", ref, got, want)
		}
	}
}

func TestDockerImagesOfAScript(t *testing.T) {
	script := "docker build -t ghcr.io/org/app-cli:dev .\ndocker push ghcr.io/org/app-cli:dev\n" +
		"docker buildx build --push \\\n  --tag \"ghcr.io/org/app-web:1\" .\necho -t ghcr.io/org/not-docker\n"
	got := dockerImages(script)
	want := []string{"ghcr.io/org/app-cli:dev", "ghcr.io/org/app-cli:dev", "ghcr.io/org/app-web:1"}
	if len(got) != len(want) {
		t.Fatalf("dockerImages = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("dockerImages[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
