# Script

The script potion type can refer to :
- an actual script file (script.file)
- a script command (script.command)

## script.file

### Input
The body of a script file potion includes : 
- a shell type. If the shell is not installed on the user's machine, the script will fail. You have to manually add the shell dependency in the dependencies.yaml file for the same circle. 
- a file path. This path has to be a relative path
- an array of arguments. Arguments can be a string, number or a rune following the runes name convention (`${rune:XX}`). If the argument is a rune, the rune will be resolved before the script is executed.

### Output
The script.file potion returns the output of the script as a string, that can be accessed through the id of the potion `${launch_a_script.output}`.
If the script fails, it will print the reason and continue/stop, depending on the fail-stop property of the potion.

### Example
Here is an example of a script.file potion :
```yaml
launch_a_script:
    label: "Check something"
    fail-stop: true
    incantation: "Checking for something"
    type: script.file
    body:
        shell: bash | powershell | cmd 
        file: ../path/to/scripts/xxx.sh
        args:
            - "arg1"
            - "arg2"
            - "${rune:TOKEN}"
```


## script.command


```yaml
launch_a_script:
    label: "Check"
    incantation: "Checking for something"
    type: script.command
    body:
        cmd: ['bash','-c', '../path/to/scripts/xxx.sh', 'arg1', 'arg2', '${rune:TOKEN}']
        export:
```