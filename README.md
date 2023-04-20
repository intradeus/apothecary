## Apothecary
Easily concoct local development environment of microservices infrastructure (IaaC tool)

### Ideas :
- vault : plugins for onepass, lastpass, windows credential, custom etc.
- plugin integration with version control system (git, mercure)
- traduction
- gestion de version des dépendances (ex: limiter docker à minimum 22.11.0 et maximum 22.30.0)

### Initial arguments: 
##### Initialize a new grimoire
```
apocker create --name entreprise_grimoires --circles developers,devops
```
We suggest adding your grimoire to a github repo, to share the configs for your team

##### Run the grimoires
```bash
apocker test --grimoires entreprise_grimoires --circle developers
apocker remember -g entreprise_grimoires -c developers # Set the .grimoire.config
apocker init -g entreprise_grimoires -c developers
apocker start -g entreprise_grimoires -c developers
apocker stop -g entreprise_grimoires -c developers
apocker forget -g -c # Reset the .grimoires file to null for values passed
?? apocker pull # If the grimoire is pushed to github, this will pull the latest version
```


## Code classes
- reader : will validate and get config values from a .yml file (launch.yml, devinit.yml, stop.yml)
- validation: validates the yaml files
- update : will fetch the latest git pull from the config repo
- selector: user selection of services
- vault: tokens encryption, (injected into the config as ${vault:GITHUB_TOKEN} : requires a pin/password on first launch, try to make it so it asks it only when computer turned off

A circle has 1 or many grimoires. A grimoire has one or many potions. A potion has a type
## File structure
```
entreprise
├── entreprise_repo_1
├── entreprise_repo_2
├── entreprise_repo_3
├── entreprise_databases
├── entreprise_grimoires
│   ├── circles
│   │   ├── default
│   │   │   ├── apo.runes
│   │   │   ├── grimoires
│   │   │   │   ├── init.yml
│   │   │   │   ├── start.yml
│   │   │   │   ├── stop.yml
│   │   ├── frontend-dev
│   │   │   ├── apo.runes
│   │   │   ├── grimoires
│   │   │   │   ├── init.yml
│   │   │   │   ├── start.yml
│   │   │   │   ├── stop.yml
│   │   ├── devops
│   │   │   ├── apo.runes
│   │   │   ├── grimoires
│   │   │   │   ├── init.yml
│   │   │   │   ├── start.yml
│   │   │   │   ├── stop.yml
│   ├── scripts
│   │   ├── xxx.sh
│   │   ├── yyy.sh
│   ├── databases
│   │   ├── docker-compose.yml
│   │   ├── .env
│   ├── miscellaneous
│   │   ├── docker-compose.yml
│   │   ├── .env
├── .grimoires
```

## Config file


dev/launch.yml
```yml
# ALL PATHS ARE RELATIVE TO THIS YAML FILE

runes: ./apodocker.runes # encrypted vault, injected as ${rune:XYZ}

# Each potion is a group. The user will choose for each potion what he wants to launch, if selection = 'user' 
potions: 
	create_network:
		type: docker.network # will check if network exists and creates it if necessary.
		label: "Networks"
		incantation: "Creating network"
		body:
			backend:
				overwrite: false # if true : deletes existing network and create new
				name: project_backend_network
				options:
					- "--driver bridge"
					- "--attachable"
			frontend:
				overwrite: true 
				name: project_frontend_network
				options:
					- "--driver bridge"
					- "--attachable"

	launch_databases:
		type: docker-compose # fetch a docker-compose file and launch it
		label: "Databases"
		incantation: "Launching databases"
		body:
			location: ../databases/docker-compose.yml
			# herbs overwrites the global herbs for this potion, NOT IN THE DOCKER-COMPOSE FILE !! use the --env-file options for this (or place a .env next to the docker-compose file)
			herbs: ./db/.env 
			detached: true
			# all services, user selected service or default services
			selection: 'all'|'user'|'default' 
			default:
				- mysql_test
				- sqlite_reader
			options:
				- "-p project-databases"
				- "--env-file ../databases/.env"
				- "--profiles test"

	launch-tools:
		type: docker # fetch  docker image and run them
		label: "Tools"
		incantation: "Launching developer tools"
		ingredient-selection: 'all'|'user'
		herbs: ../misc/.env
		list:
			phpmyadmin:
				name: "PhpMyAdmin"
				image: phpmyadmin:5
				options:
					- "--name entreprise-phpmyadmin"
					- "-e PMA_PASSWORD=${herb:PMA}"
					- "--network entreprise_frontend_network"
			mock-server:
				name: "PhpMyAdmin"
				image: mock-server-api:latest
				options:
					- "--name entreprise-phpmyadmin"
					- "-e PMA_PASSWORD=${herb:PMA}"
					- "--network entreprise_frontend_network"

	check_for_something:
		type: script
		label: "Check"
		incantation: "Checking for something"
		ingredient-selection: 'all'|'user'
		cmd: ['bash','-c', '../scripts/xxx.sh', 'arg1','${herb:PERS_TOKEN}']

	launch_second_services:
		description: "Launching first projects"
		type: vscode # goes to a folder and open vscode
		folders: 
			- '../entreprise_repos/microservice_1'
			- '../entreprise_repos/microservice_2'

	launch_third_services:
		description: "Launching second projects"
		type: devcontainer # devcontainer cli of vscode, open in container directly
		folders:
			- '../entreprise_repos/microservice_3'
			- '../entreprise_repos/microservice_4'

preparation: # If needed, specify the order of the potions here
	- create_network
	- launch_second_services
	- launch_databases
	- launch_first_service
	- launch_third_services
```


dev/init.yml
```yml
# ALL PATHS ARE RELATIVE TO THIS YAML FILE
grimoire:
	name: ENTREPRISE
	herbs: ./.env # injects .env into this config file as ${config:XYZ} 
	runes: ./apodocker.runes # this is your encrypted vault

```
