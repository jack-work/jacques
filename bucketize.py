import pandas as pd
import numpy as np

df = pd.read_csv(r"C:\Users\jokellih\dev\jacques\env_elo_impact.csv")

bucket_edges = [0, 5, 10, 25, 50, 100, 250, 500, 1000, np.inf]
labels = ["1-5", "6-10", "11-25", "26-50", "51-100", "101-250", "251-500", "501-1K", "1K+"]
df["bucket"] = pd.cut(df["operation_count"], bins=bucket_edges, labels=labels, right=True)

summary = df.groupby("bucket", observed=False).agg(
    rows=("operation_count", "size"),
    unique_tenants=("tenant_id", "nunique"),
    unique_envs=("env_id", "nunique"),
    total_ops=("operation_count", "sum"),
).reset_index()

summary["row_pct"] = (summary["rows"] / summary["rows"].sum() * 100).round(1)
summary["ops_pct"] = (summary["total_ops"] / summary["total_ops"].sum() * 100).round(1)
summary["cum_row_pct"] = summary["row_pct"].cumsum().round(1)
summary["cum_ops_pct"] = summary["ops_pct"].cumsum().round(1)

print("=== Bucket Distribution ===")
print(f"{'bucket':<10} {'rows':>6} {'row%':>6} {'cum_r%':>7} {'ops':>8} {'ops%':>6} {'cum_o%':>7} {'tenants':>8} {'envs':>6}")
print("-" * 75)
for _, r in summary.iterrows():
    print(f"{r['bucket']:<10} {r['rows']:>6} {r['row_pct']:>5.1f}% {r['cum_row_pct']:>6.1f}% {r['total_ops']:>8} {r['ops_pct']:>5.1f}% {r['cum_ops_pct']:>6.1f}% {r['unique_tenants']:>8} {r['unique_envs']:>6}")

print(f"\n{'TOTAL':<10} {summary['rows'].sum():>6} {'':>6} {'':>7} {summary['total_ops'].sum():>8}")

summary.to_csv(r"C:\Users\jokellih\dev\jacques\env_elo_impact_buckets.csv", index=False)
print("\nWrote env_elo_impact_buckets.csv")
