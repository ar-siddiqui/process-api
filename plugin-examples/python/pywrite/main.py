import os
import sys
import json
from datetime import datetime, timedelta
import boto3

# from dotenv import load_dotenv


def write_text_to_s3_file(s3_client, bucket: str, text: str, s3_key: str, content_type: str, exp_days: int = 0):
    print(f"writing `{s3_key}` to `{bucket}`")
    if exp_days:
        expiration_time = datetime.now() + timedelta(days=exp_days)
        s3_client.put_object(Bucket=bucket, Key=s3_key, Body=text, ContentType=content_type, Expires=expiration_time)
    else:
        s3_client.put_object(Bucket=bucket, Key=s3_key, Body=text, ContentType=content_type)


def create_presigned_url(bucket, s3_key: str, exp_days: int = 7) -> str:
    if not exp_days:  # handle 0 expiry days
        exp_days = 7

    expiration_time = datetime.now() + timedelta(days=exp_days)

    presigned_url = s3_client.generate_presigned_url(
        "get_object",
        Params={"Bucket": bucket, "Key": s3_key},
        ExpiresIn=int((expiration_time - datetime.now()).total_seconds()),
    )
    return presigned_url


def parse_input(params_string: str) -> str:
    try:
        params = json.loads(params_string)
        if not params["userInput"]:
            print("input json must include a `userInput` field")
            sys.exit(1)
        if not params["outputFile"]:
            print("input json must include a `userInput` field")
            sys.exit(1)
        return params["userInput"], params["outputFile"]
    except Exception as e:
        print("Error:", str(e))
        sys.exit(1)


def print_plugin_results(output_file: str, presigned_url: str):
    print(
        {
            "plugin_results": {
                "textFile": output_file,
                "ref": presigned_url,
            }
        }
    )


def verify_env():
    """TODO: verify these are checked"""
    if "MINIO_ACCESS_KEY_ID" not in os.environ:
        print("missing enviornment variable: `MINIO_ACCESS_KEY_ID` required")
        sys.exit(1)
    if "MINIO_SECRET_ACCESS_KEY" not in os.environ:
        print("missing enviornment variable: `MINIO_ASECRET_ACCESS_KEY` required")
        sys.exit(1)
    if "MINIO_S3_REGION" not in os.environ:
        print("missing enviornment variable: `MINIO_S3_REGION` required")
        sys.exit(1)
    if "MINIO_S3_BUCKET" not in os.environ:
        print("missing enviornment variable: `MINIO_S3_BUCKET` required")
        sys.exit(1)
    if "MINIO_S3_ENDPOINT" not in os.environ:
        print("missing enviornment variable: `MINIO_S3_ENDPOINT` bool required")
        sys.exit(1)


def init_s3_client():
    s3_config = {
        "aws_access_key_id": os.environ["MINIO_ACCESS_KEY_ID"],
        "aws_secret_access_key": os.environ["MINIO_SECRET_ACCESS_KEY"],
        "endpoint_url": os.environ["MINIO_S3_ENDPOINT"],
        "region_name": os.environ["MINIO_S3_REGION"],
        "use_ssl": False,
    }

    bucket = os.environ["MINIO_S3_BUCKET"]
    s3_client = boto3.client("s3", **s3_config)
    return s3_client, bucket


if __name__ == "__main__":
    # Load environment variables from the .env file in the current directory
    print("initializing pywrite plugin")

    # load_dotenv()
    verify_env()
    """
    exptected arg: '{"jobID": "sadf234sdf234sdf", "userInput": "hello!", "outputFile":"pywrite/outputs/demo.txt"}'
    expected response: {"plugin_outputs": {"message": "hello! from pyecho"}}
    """
    if len(sys.argv) == 2 and (sys.argv[1] == "--help" or sys.argv[1] == "-h"):
        print(__doc__)
        sys.exit(1)

    if len(sys.argv) != 2:
        print(
            """Error: required input missing. \example usage: main.py'{"jobID": "sadf234sdf234sdf", "userInput": "hello!", "outputFile":"pywrite/outputs/demo.txt"}'"""
        )
        sys.exit(1)

    message, output_file = parse_input(params_string=sys.argv[-1])
    s3_client, bucket = init_s3_client()

    try:
        write_text_to_s3_file(s3_client, bucket, message, output_file, "text/plain")
    except Exception as e:
        print("failed writing to s3. Verify minio credentials / bucket access:", e)
        sys.exit(1)

    try:
        presigned_url = create_presigned_url(bucket, output_file, exp_days=7)
    except Exception as e:
        print("failed creating a temp download link:", e)
        sys.exit(1)

    try:
        print_plugin_results(output_file, presigned_url)
    except Exception as e:
        print("failed printing results output to container log", e)
        sys.exit(1)
