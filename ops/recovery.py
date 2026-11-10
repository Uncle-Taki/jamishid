#!/usr/bin/env python3
"""Consistent PostgreSQL backups and restore drills in generated disposable databases."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import uuid
CONTAINER = ''
USER = 'jamshid'
DATABASE = 'jamshid'
STATE = Path('recovery-artifacts')
TABLES = ['schema_migrations', 'crawl_searches', 'crawl_jobs', 'crawl_tasks', 'listings', 'search_listings', 'listing_observations', 'detail_tasks', 'job_details', 'listing_events']
def configure(container,user,database,directory):
 global CONTAINER,USER,DATABASE,STATE
 CONTAINER,USER,DATABASE,STATE=container,user,database,Path(directory).resolve()
 if not container:raise ValueError('A PostgreSQL container is required.')
def run(args, *, stdin=None, stdout=None, timeout=300):
 result=subprocess.run(args,input=stdin,stdout=stdout if stdout is not None else subprocess.PIPE,stderr=subprocess.PIPE,timeout=timeout)
 if result.returncode:raise RuntimeError(result.stderr.decode(errors='replace')[-3000:])
 return result.stdout or b''
def save(path,value):
 path.parent.mkdir(parents=True,exist_ok=True,mode=0o700)
 temporary=path.with_suffix('.tmp');temporary.write_text(json.dumps(value,indent=2)+'\n');temporary.chmod(0o600);temporary.replace(path)
def name(value):
 if not re.fullmatch(r'[a-z0-9][a-z0-9-]{0,60}',value):raise ValueError('Invalid backup name.')
 return value
def pg_args(database, tool='psql'):
    return ['docker','exec','-i','-e','PGOPTIONS=-c timezone=UTC',CONTAINER,tool,'-U',USER,'-d',database]


def fingerprint_sql():
    entries = []
    for table in TABLES:
        # Hash per-row JSON before aggregation, avoiding aggregation of full listing bodies.
        entries.extend(["'"+table+"'",f"(SELECT jsonb_build_object('count',count(*),'digest',md5(coalesce(string_agg(row_hash,'' ORDER BY row_hash),''))) FROM (SELECT md5(to_jsonb(t)::text) row_hash FROM public.{table} t) hashed)"])
    return 'SELECT jsonb_build_object('+','.join(entries)+');'


def backup(backup_name):
    name(backup_name)
    STATE.mkdir(parents=True,exist_ok=True,mode=0o700)
    archive=STATE/(backup_name+'.dump')
    metadata=STATE/(backup_name+'.backup.json')
    if archive.exists() or metadata.exists():
        raise ValueError('Backup already exists; choose a new name.')
    database=DATABASE
    session=subprocess.Popen(pg_args(database)+['-X','-qAt','-v','ON_ERROR_STOP=1'],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
    try:
        session.stdin.write(b'BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY; SELECT pg_export_snapshot();\n');session.stdin.flush()
        snapshot=session.stdout.readline().decode().strip()
        if not re.fullmatch(r'[0-9A-Fa-f-]+',snapshot):
            raise RuntimeError('Could not export a consistent database snapshot.')
        session.stdin.write((fingerprint_sql()+'\n').encode());session.stdin.flush()
        expected=json.loads(session.stdout.readline())
        with archive.open('xb') as output:
            archive.chmod(0o600)
            run(pg_args(database,'pg_dump')+['-Fc','--no-owner','--no-acl','--snapshot='+snapshot],stdout=output)
        session.stdin.write(b'COMMIT;\n');session.stdin.close();session.wait(timeout=10)
        if session.returncode:
            raise RuntimeError('Backup snapshot transaction failed.')
        value={'archive':str(archive),'sha256':hashlib.sha256(archive.read_bytes()).hexdigest(),'sourceDatabase':database,'expected':expected}
        save(metadata,value)
        return metadata
    except Exception:
        archive.unlink(missing_ok=True)
        raise
    finally:
        if session.poll() is None:
            session.terminate();session.wait(timeout=10)


def restore_drill(metadata):
    value=json.loads(Path(metadata).read_text())
    archive=Path(value['archive'])
    if hashlib.sha256(archive.read_bytes()).hexdigest()!=value['sha256']:
        raise ValueError('Backup checksum mismatch.')
    database='jamshid_restore_'+uuid.uuid4().hex[:12]
    user=USER
    admin=['docker','exec','-i',CONTAINER]
    created=False
    try:
        run(admin+['createdb','-U',user,database]);created=True
        with archive.open('rb') as stream:
            result=subprocess.run(pg_args(database,'pg_restore')+['--exit-on-error','--no-owner','--no-acl'],stdin=stream,stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=180)
        if result.returncode:
            raise RuntimeError('Restore failed: '+result.stderr.decode(errors='replace')[-2000:])
        restored=json.loads(run(pg_args(database)+['-X','-qAt','-v','ON_ERROR_STOP=1','-c',fingerprint_sql()]))
        if restored!=value['expected']:
            raise RuntimeError('Restored table counts/digests differ from the exported snapshot.')
        report={'status':'passed','backup':str(metadata),'isolatedDatabase':database,'tables':restored}
        save(Path(metadata).with_suffix('.restore.json'),report)
        return report
    finally:
        if created:
            # The target is generated here, never accepted as a caller-supplied database.
            run(admin+['dropdb','-U',user,'--force',database])


def main():
 parser=argparse.ArgumentParser(description=__doc__)
 parser.add_argument('--container',required=True);parser.add_argument('--user',default='jamshid');parser.add_argument('--database',default='jamshid');parser.add_argument('--directory',default='recovery-artifacts');parser.add_argument('--name',default='drill-'+uuid.uuid4().hex[:12]);parser.add_argument('--restore-backup')
 args=parser.parse_args();configure(args.container,args.user,args.database,args.directory)
 try:
  metadata=Path(args.restore_backup) if args.restore_backup else backup(args.name)
  report=restore_drill(metadata);print(json.dumps(report))
 except Exception as error:parser.exit(1,str(error)+'\n')
if __name__=='__main__':main()
