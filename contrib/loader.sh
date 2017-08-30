#!/bin/bash

function start {
  echo -e "Starting first server.\n"
  ./dgraph -p $BUILD/p -w $BUILD/w -memory_mb 2048 --group_conf groups.conf --groups "0,1" --idx 1 --my "127.0.0.1:12345" > $BUILD/server.log &
  sleep 5
  echo -e "Starting second server.\n"
  ./dgraph -p $BUILD/p2 -w $BUILD/w2 -memory_mb 2048 --group_conf groups.conf --groups "2" --idx 2 --my "127.0.0.1:12346" --peer "127.0.0.1:12345" --port 8082 --grpc_port 9082 --workerport 12346 > $BUILD/server2.log &
  return 0
}


SRC="$( cd -P "$( dirname "${BASH_SOURCE[0]}" )" && pwd )/.."

BUILD=$1
# If build variable is empty then we set it.
if [ -z "$1" ]; then
  BUILD=$SRC/build
fi

set -e

pushd $BUILD &> /dev/null

if [ ! -f "goldendata.rdf.gz" ]; then
  echo -e "\nDownloading goldendata."
  wget -q https://github.com/dgraph-io/benchmarks/raw/master/data/goldendata.rdf.gz
fi

# log file size.
ls -la goldendata.rdf.gz

benchmark=$(pwd)
popd &> /dev/null

pushd cmd/dgraph &> /dev/null
echo -e "\nBuilding and running Dgraph."
go build .

./dgraph --version
if [ $? -eq 0 ]; then
  echo -e "dgraph --version succeeded.\n"
else
  echo "dgraph --version command failed."
fi

# Start Dgraph
start
popd &> /dev/null

sleep 10

#Set Schema
curl -X POST  -d 'mutation {
schema {
name: string @index(term) .
_xid_: string @index(exact,term) .
initial_release_date: datetime @index(year) .
  }
}' "http://localhost:8080/query"

echo -e "\nBuilding and running dgraphloader."
pushd cmd/dgraphloader &> /dev/null
# Delete client directory to clear checkpoints.
rm -rf c
go build .
./dgraphloader -r $benchmark/goldendata.rdf.gz -x true
popd &> /dev/null

pushd $GOPATH/src/github.com/dgraph-io/dgraph/contrib/indextest &> /dev/null

function quit {
  curl localhost:8080/admin/shutdown
  curl localhost:8082/admin/shutdown
  return $1
}

function run_index_test {
  local max_attempts=${ATTEMPTS-5}
  local timeout=${TIMEOUT-1}
  local attempt=0
  local exitCode=0

  X=$1
  GREPFOR=$2
  ANS=$3
  echo "Running test: ${X}"
  while (( $attempt < $max_attempts ))
  do
    set +e
    N=`curl -s localhost:8080/query -XPOST -d @${X}.in`
    exitCode=$?

    set -e

    if [[ $exitCode == 0 ]]
    then
      break
    fi

    echo "Failure! Retrying in $timeout.." 1>&2
    sleep $timeout
    attempt=$(( attempt + 1 ))
    timeout=$(( timeout * 2 ))
  done

  NUM=$(echo $N | python -m json.tool | grep $GREPFOR | wc -l)
  if [[ ! "$NUM" -eq "$ANS" ]]; then
    echo "Index test failed: ${X}  Expected: $ANS  Got: $NUM"
    quit 1
  else
    echo -e "Index test passed: ${X}\n"
  fi
}

echo -e "\nRunning some queries and checking count of results returned."
run_index_test basic name 138676
run_index_test allof_the name 25431
run_index_test allof_the_a name 367
run_index_test allof_the_first name 4383
run_index_test releasedate release_date 137858
run_index_test releasedate_sort release_date 137858
run_index_test releasedate_sort_first_offset release_date 2315
run_index_test releasedate_geq release_date 60991
run_index_test gen_anyof_good_bad name 1103

popd &> /dev/null

echo -e "\nShutting down Dgraph"
quit 0

# Wait for clean shutdown.
sleep 20

echo -e "\nTrying to restart Dgraph and match export count"
pushd cmd/dgraph &> /dev/null
start
# Wait to become leader.
sleep 5
echo -e "\nTrying to export data."
curl http://localhost:8080/admin/export
echo -e "\nExport done."

# This is count of RDF's in goldendata.rdf.gz + xids because we ran dgraphloader with -xid flag.
dataCount="1475250"
# Concat exported files to get total count.
cat $(ls -t export/dgraph-1-* | head -1) $(ls -t export/dgraph-2-* | head -1) > dgraph-export.rdf.gz
if [[ $TRAVIS_OS_NAME == "osx" ]]; then
  exportCount=$(export/dgraph-export.rdf.gz | wc -l)
else
  exportCount=$(export/dgraph-export.rdf.gz | wc -l)
fi


if [[ ! "$exportCount" -eq "$dataCount" ]]; then
  echo "Export test failed. Expected: $dataCount Got: $exportCount"
  quit 1
fi

echo -e "All tests ran fine. Exiting"
quit 0
