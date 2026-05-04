import pandas as pd
import numpy as np

df = pd.read_csv(
    r"C:\Users\jokellih\dev\orchard\jokellih\acis\test",
    sep="\t",
    header=None,
    names=["tenant_id", "env_id", "n1", "n2", "n3"],
)

df["total"] = df["n1"] + df["n2"] + df["n3"]

# --- basic stats ---
print("=== Basic Stats ===")
print(df["total"].describe())
print(f"\nSkewness:  {df['total'].skew():.4f}")
print(f"Kurtosis:  {df['total'].kurtosis():.4f}")

# --- percentile table ---
pcts = [50, 75, 90, 95, 99, 99.5, 99.9, 100]
print("\n=== Percentiles ===")
for p in pcts:
    print(f"  P{p:<5} = {np.percentile(df['total'], p):>10.0f}")

# --- bucketize ---
bucket_edges = [0, 10, 50, 100, 250, 500, 1000, 2500, 5000, np.inf]
labels = ["0-10", "11-50", "51-100", "101-250", "251-500",
          "501-1K", "1K-2.5K", "2.5K-5K", "5K+"]
df["bucket"] = pd.cut(df["total"], bins=bucket_edges, labels=labels, right=True)

# --- counts per bucket ---
print("\n=== Row count per bucket ===")
bucket_counts = df["bucket"].value_counts().sort_index()
for b, c in bucket_counts.items():
    print(f"  {b:<10}  {c:>6}  ({c / len(df) * 100:5.1f}%)")

# --- unique tenant/env counts per bucket ---
print("\n=== Unique tenant_id count per bucket ===")
tenant_per_bucket = df.groupby("bucket", observed=False)["tenant_id"].nunique()
for b, c in tenant_per_bucket.items():
    print(f"  {b:<10}  {c:>6}")

print("\n=== Unique env_id count per bucket ===")
env_per_bucket = df.groupby("bucket", observed=False)["env_id"].nunique()
for b, c in env_per_bucket.items():
    print(f"  {b:<10}  {c:>6}")

# --- cumulative: what % of rows are below threshold ---
print("\n=== Cumulative distribution (rows below threshold) ===")
thresholds = [10, 25, 50, 100, 250, 500, 1000, 2000, 5000]
for t in thresholds:
    below = (df["total"] <= t).sum()
    print(f"  <= {t:<5}  {below:>6} rows  ({below / len(df) * 100:5.1f}%)")

# --- top 20 heaviest rows ---
print("\n=== Top 20 rows by total ===")
top = df.nlargest(20, "total")[["tenant_id", "env_id", "n1", "n2", "n3", "total"]]
print(top.to_string(index=False))
