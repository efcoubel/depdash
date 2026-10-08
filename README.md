# DepDash

Un conteneur Docker qui scanne des projets locaux et affiche, dans un tableau de bord web, où en est chacun par rapport aux dernières versions de son langage, de son framework et de ses dépendances.

DepDash est passif : le dossier des projets est monté en lecture seule, rien n'y est écrit, aucune PR n'est créée. Seuls des noms de paquets publics sortent vers les registres.

## Démarrer

Dans `docker-compose.yml`, faire pointer le volume `/projects` vers le dossier qui contient vos repos, puis :

```bash
docker compose up -d
```

Le tableau de bord est sur <http://localhost:8080>. Un premier scan part au démarrage, puis toutes les 6 heures.

Sans Docker :

```bash
go build -o bin/depdash ./cmd/depdash
```

```bash
DEPDASH_PROJECTS_DIR=~/projets DEPDASH_DATA_DIR=./data ./bin/depdash serve
```

| Commande | Rôle |
| --- | --- |
| `depdash serve` | Tableau de bord et scans périodiques (défaut) |
| `depdash scan` | Un scan, puis l'inventaire dans le terminal ; `-offline` n'interroge pas les registres |
| `depdash healthcheck` | Sonde `/healthz`, utilisée par le `HEALTHCHECK` de l'image |

## Ce qui est détecté

| | PHP / Composer | Node / npm |
| --- | --- | --- |
| Manifeste | `composer.json` | `package.json` |
| Versions installées | `composer.lock` | `package-lock.json` (v1, v2, v3) |
| Runtime, par priorité | `.php-version`, `FROM php:X`, `config.platform.php`, `require.php` | `.nvmrc`, `.node-version`, `engines.node`, `FROM node:X` |
| Framework | Symfony, Laravel | Next.js, Nuxt, Angular, React, Vue |

Un repo est le dossier qui contient `.git` ; il peut regrouper plusieurs projets (monorepo). Sans lockfile, la contrainte du manifeste est affichée et le statut est estimé à partir d'elle.

## Statuts

Le statut n'est pas stocké : il est recalculé à chaque affichage.

| Statut | Règle |
| --- | --- |
| À jour | Dernière version, ou même mineure que la dernière |
| Mise à jour mineure | Même majeure, mineure en retard |
| Majeure disponible | Une majeure plus récente existe, version actuelle encore supportée |
| Fin de support proche | Fin du support sécurité dans moins de 6 mois (`DEPDASH_EOL_WARNING`) |
| Non supportée | Fin du support sécurité dépassée |
| Inconnu | Paquet introuvable ou version non résolue |

Pour un runtime ou un framework, une LTS encore supportée est « à jour » même si une majeure plus récente existe.

## Configuration

| Variable | Défaut | Rôle |
| --- | --- | --- |
| `DEPDASH_PROJECTS_DIR` | `/projects` | Dossier scanné |
| `DEPDASH_DATA_DIR` | `/data` | Base SQLite |
| `DEPDASH_PORT` | `8080` | Port HTTP |
| `DEPDASH_SCAN_INTERVAL` | `6h` | Fréquence du scan automatique |
| `DEPDASH_MAX_DEPTH` | `4` | Profondeur de recherche des manifestes |
| `DEPDASH_EOL_WARNING` | `180d` | Seuil « fin de support proche » |
| `DEPDASH_IGNORE` | — | Dossiers ignorés en plus de `vendor`, `node_modules`, `.git`, `var`, `dist`, `build` (séparés par des virgules) |
| `DEPDASH_MAPPING_FILE` | `$DEPDASH_DATA_DIR/mapping.json` | Table paquet → produit endoflife.date |

Pour reconnaître un autre framework, copier [`internal/config/mapping.json`](internal/config/mapping.json) vers `data/mapping.json` et y ajouter une entrée. Sans ce fichier, la table embarquée s'applique.

## Routes

| Méthode | Route | Rôle |
| --- | --- | --- |
| GET | `/` | Vue d'ensemble |
| GET | `/projects/{id}` | Vue projet |
| GET | `/packages/{ecosystem}/{name}` | Vue transversale d'un paquet |
| POST | `/scan` | Rescan global |
| POST | `/projects/{id}/scan` | Rescan d'un projet |
| POST | `/projects/{id}/manual` | Saisie manuelle |
| GET | `/api/projects` | Export JSON |
| GET | `/healthz` | Healthcheck |

Il n'y a pas d'authentification : `docker-compose.yml` publie le port sur `127.0.0.1` seulement, et les requêtes POST venant d'un autre site sont refusées.

## Sources de données

| Donnée | Source | Cache |
| --- | --- | --- |
| Versions d'un paquet PHP | `repo.packagist.org/p2/{vendor}/{package}.json` | 6 h |
| Versions d'un paquet npm | `registry.npmjs.org/{package}` | 6 h |
| Cycles de vie | `endoflife.date/api/{product}.json` | 24 h |

Si une API est indisponible, la dernière valeur en cache reste affichée avec sa date. Chaque registre reçoit au plus 8 requêtes simultanées, avec 10 s de délai et 2 tentatives.

## Développement

```bash
go test -race -cover ./...
```

```
cmd/depdash/         point d'entrée, assemblage
internal/config/     variables d'environnement, table de correspondance
internal/scanner/    parcours du dossier, détection des projets
internal/parser/     interface Parser ; composer/, npm/
internal/registry/   interfaces Registry et Lifecycle ; packagist/, npm/, endoflife/
internal/status/     calcul des statuts
internal/store/      SQLite et migrations
internal/enrich/     remplissage des caches depuis les registres
internal/scheduler/  scans périodiques et à la demande
internal/web/        handlers HTTP, templates, assets embarqués
testdata/            manifestes et lockfiles de test
```

Ajouter un écosystème : écrire un `Parser` et un `Registry`, puis les déclarer dans `app()` de [`cmd/depdash/main.go`](cmd/depdash/main.go). Le scanner, l'enrichisseur et l'interface web n'ont pas à changer.
