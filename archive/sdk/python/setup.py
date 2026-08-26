from setuptools import setup, find_packages

setup(
    name="spine-engine",
    version="3.0.0",
    description="Python Client SDK for Spine — Declarative Event-Driven Backend Engine",
    author="Amrit Rai",
    author_email="amritrai@example.com",
    url="https://github.com/AmritRai1234/spine",
    packages=find_packages(),
    install_requires=[
        "requests>=2.25.0",
        "websocket-client>=1.0.0",
    ],
    classifiers=[
        "Programming Language :: Python :: 3",
        "License :: OSI Approved :: GNU Lesser General Public License v3 (LGPLv3)",
        "Operating System :: OS Independent",
    ],
    python_requires=">=3.7",
)
