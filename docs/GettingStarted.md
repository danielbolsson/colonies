# Getting started
## Installation
```console
wget https://github.com/colonyos/colonies/blob/main/bin/colonies?raw=true -O /bin/colonies
chmod +x /bin/colonies
```

## Development server
The development server is for testing only. All data will be lost when restarting the server. Also note that all keys are well known and all data is sent over unencrypted HTTP.

You will have to export some environmental variables in order to use the development server. Add the following variables to your shell.

```console
export LANG=en_US.UTF-8
export LANGUAGE=en_US.UTF-8
export LC_ALL=en_US.UTF-8
export LC_CTYPE=UTF-8
export TZ=Europe/Stockholm
export COLONIES_TLS="false"
export COLONIES_SERVERHOST="localhost"
export COLONIES_SERVERPORT="50080"
export COLONIES_MONITORPORT="21120"
export COLONIES_MONITORINTERVALL="1"
export COLONIES_SERVERID="039231c7644e04b6895471dd5335cf332681c54e27f81fac54f9067b3f2c0103"
export COLONIES_SERVERPRVKEY="fcc79953d8a751bf41db661592dc34d30004b1a651ffa0725b03ac227641499d"
export COLONIES_DBHOST="localhost"
export COLONIES_DBUSER="postgres"
export COLONIES_DBPORT="50070"
expoer COLONIES_DBPASSWORD="rFcLGNkgsNtksg6Pgtn9CumL4xXBQ7"
export COLONIES_COLONYID="4787a5071856a4acf702b2ffcea422e3237a679c681314113d86139461290cf4"
export COLONIES_COLONYPRVKEY="ba949fa134981372d6da62b6a56f336ab4d843b22c02a4257dcf7d0d73097514"
export COLONIES_RUNTIMEID="3fc05cf3df4b494e95d6a3d297a34f19938f7daa7422ab0d4f794454133341ac"
export COLONIES_RUNTIMEPRVKEY="ddf7f7791208083b6a9ed975a72684f6406a269cfa36f1b1c32045c0a71fff05"
export COLONIES_RUNTIMETYPE="cli"
```
or 
```console
source examples/devenv
```

Now, start the development server. The development server will automatically add the keys from the environment (e.g. COLONIES_RUNTIMEPRVKEY) to the Colonies keychain.

```console
colonies dev
```

## Start a worker
Open another terminal (and *source examples/devenv*).

```console
colonies worker start --name myworker --type cli 
```
## Submit a process specification
Example process specification (see examples/sleep.json). The Colonies worker will pull the process specification from the Colonies dev server and start a *sleep* process. This will cause the worker above to sleep for 100s. The *env* array in the JSON below will automatically be exported as real environment variables in the sleep process.
```json
{
  "conditions": {
    "runtimetype": "cli"
  },
  "func": "sleep",
  "args": [
    "3"
  ],
  "env": {
    "TEST": "testenv"
  }
}
```

Open another terminal (and *source examples/devenv*).
```console
colonies process submit --spec sleep.json
```

Alternatively,
```console
colonies process run --func sleep --args 3 --runtimetype cli  
```

Check out running processes:
```console
colonies process ps
+------------------------------------------------------------------+-------+------+---------------------+----------------+
| ID                                                               | FUNC  | ARGS | START TIME          | TARGET RUNTIME |
+------------------------------------------------------------------+-------+------+---------------------+----------------+
| 6681946db095e0dc2e0408b87e119c0d2ae4f691db6899b829161fc97f14a1d0 | sleep | 3    | 2022-04-05 16:40:01 | cli            |
+------------------------------------------------------------------+-------+------+---------------------+----------------+
```

Check out process status: 
```console
colonies process get --processid 6681946db095e0dc2e0408b87e119c0d2ae4f691db6899b829161fc97f14a1d0
Process:
+-------------------+------------------------------------------------------------------+
| ID                | 6681946db095e0dc2e0408b87e119c0d2ae4f691db6899b829161fc97f14a1d0 |
| IsAssigned        | True                                                             |
| AssignedRuntimeID | 66f55dcb577ca6ed466ad5fcab868673bc1fc7d6ea7db71a0af4fea86035c431 |
| State             | Running                                                          |
| SubmissionTime    | 2022-04-05 16:40:00                                              |
| StartTime         | 2022-04-05 16:40:01                                              |
| EndTime           | 0001-01-01 01:12:12                                              |
| Deadline          | 0001-01-01 01:12:12                                              |
| WaitingTime       | 753.441ms                                                        |
| ProcessingTime    | 1m23.585764152s                                                  |
| Retries           | 0                                                                |
+-------------------+------------------------------------------------------------------+

ProcessSpec:
+-------------+-------+
| Func        | sleep |
| Args        | 3     |
| MaxExecTime | -1    |
| MaxRetries  | 0     |
+-------------+-------+

Conditions:
+-------------+------------------------------------------------------------------+
| ColonyID    | 4787a5071856a4acf702b2ffcea422e3237a679c681314113d86139461290cf4 |
| RuntimeIDs  | None                                                             |
| RuntimeType | cli                                                              |
+-------------+------------------------------------------------------------------+

Attributes:
+------------------------------------------------------------------+------+---------+------+
|                                ID                                | KEY  |  VALUE  | TYPE |
+------------------------------------------------------------------+------+---------+------+
| 2fe15f1b570c7328854f2374a69e45845ef5a40624ec06c287a51a5732485ecc | TEST | testenv | Env  |
+------------------------------------------------------------------+------+---------+------+
```

## Execution time constraints
The *maxecution* attribute specifies the maxiumum execution time in seconds before the process specification (job) is moved back to the queue. The *maxretries* attributes specifies how many times it may be moved back to the queue. Execution time constraint is an import feature of Colonies to implement robust workflows. If a worker crash, the job will automatically moved back to the queue and be executed by another worker. 

This mechanism thus offer a last line of defense against failures and enables advanched software engineering disciplines such as [Chaos Engineering](https://en.wikipedia.org/wiki/Chaos_engineering). For example, a Chaos monkey may randomly kill worker pods in Kubernetes and Colonies guarantees that all jobs are eventually executed. 

```json
{
  "conditions": {
    "runtimetype": "cli"
  },
  "func": "sleep",
  "args": [
    "100"
  ],
  "maxexectime": 5,
  "maxretries": 0,
  "env": {
    "TEST": "testenv"
  }
}
```

The process specification above will always result in failed Colonies processes as the the *sleep* process runs for exactly 100 seconds, but the process has to finish within 5 seconds. The *colonies process psf* command can be used to list all failed processes. 

```console
colonies process pss
WARN[0000] No successful processes found

colonies process psf
+------------------------------------------------------------------+-------+------+---------------------+--------------+
| ID                                                               | FUNC  | ARGS | END TIME            | RUNTIME TYPE |
+------------------------------------------------------------------+-------+------+---------------------+--------------+
| 61789512c006fc132534d73d2ce5fd4a162f9b849548fcfe300bc5b8defa6400 | sleep | 100  | 2022-05-26 17:06:24 | cli          |
+------------------------------------------------------------------+-------+------+---------------------+--------------+
```

Note that setting *maxretries* to -1 instead if 0 will result in a loop, a job that can never succeed.
